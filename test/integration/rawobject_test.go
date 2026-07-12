//go:build integration
// +build integration

/*
Copyright 2025 Guided Traffic.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package integration

import (
	"context"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/controller"
)

// rbacRawObjectConfig returns an operator config that allows the given
// namespace to render rbac Roles and ClusterRoles as raw outputs. The
// namespace is appended later (it is only known after creation), before any
// JinjaTemplate exists, so the reconciler never reads it concurrently.
func rbacRawObjectConfig() *config.OperatorConfig {
	cfg := config.NewOperatorConfig()
	cfg.RawObjectAllowlist = []config.RawObjectAllowlistEntry{
		{
			Namespaces: []string{},
			Kinds: []config.RawObjectKind{
				{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
				{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
			},
		},
	}
	return cfg
}

// waitForGone polls until the object is not found.
func waitForGone(ctx context.Context, c client.Client, key types.NamespacedName, obj client.Object) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, obj); apierrors.IsNotFound(err) {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

func TestRawObjectClusterScopedLifecycle(t *testing.T) {
	ctx := context.Background()

	cfg := rbacRawObjectConfig()
	tc := setupTestManager(t, cfg)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	cfg.RawObjectAllowlist[0].Namespaces = append(cfg.RawObjectAllowlist[0].Namespaces, ns.Name)

	crName := "raw-it-" + ns.Name

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "raw-cluster-scoped",
			Namespace: ns.Name,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ` + crName + `
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get"]
`,
			Output: jtov1.Output{Kind: controller.OutputKindRawObject},
		},
	}
	if err := tc.client.Create(ctx, jt); err != nil {
		t.Fatalf("failed to create JinjaTemplate: %v", err)
	}

	jtKey := types.NamespacedName{Name: jt.Name, Namespace: ns.Name}
	got, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue)
	if err != nil {
		t.Fatalf("failed to get JinjaTemplate: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, controller.FinalizerRawOutputCleanup) {
		t.Errorf("expected cleanup finalizer on CR, got %v", got.Finalizers)
	}
	if got.Status.LastOutput == nil || got.Status.LastOutput.APIVersion != "rbac.authorization.k8s.io/v1" ||
		got.Status.LastOutput.Kind != "ClusterRole" || got.Status.LastOutput.Name != crName {
		t.Errorf("unexpected lastOutput: %+v", got.Status.LastOutput)
	}

	// Output object exists with labels, no owner reference (cluster-scoped)
	cr := &rbacv1.ClusterRole{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: crName}, cr); err != nil {
		t.Fatalf("expected ClusterRole to exist: %v", err)
	}
	if cr.Labels[controller.LabelManagedBy] != controller.ManagerName {
		t.Errorf("expected managed-by label, got %v", cr.Labels)
	}
	if len(cr.OwnerReferences) != 0 {
		t.Errorf("cluster-scoped output must not have owner references, got %v", cr.OwnerReferences)
	}
	if len(cr.Rules) != 1 || cr.Rules[0].Resources[0] != "pods" {
		t.Errorf("unexpected rules: %+v", cr.Rules)
	}

	// Update the template: object is re-applied via SSA
	if err := tc.client.Get(ctx, jtKey, got); err != nil {
		t.Fatalf("failed to re-get JinjaTemplate: %v", err)
	}
	got.Spec.Template = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ` + crName + `
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["list"]
`
	if err := tc.client.Update(ctx, got); err != nil {
		t.Fatalf("failed to update JinjaTemplate: %v", err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := tc.client.Get(ctx, types.NamespacedName{Name: crName}, cr); err == nil &&
			len(cr.Rules) == 1 && len(cr.Rules[0].Resources) == 1 && cr.Rules[0].Resources[0] == "configmaps" {
			break
		}
		time.Sleep(interval)
	}
	if cr.Rules[0].Resources[0] != "configmaps" {
		t.Errorf("expected updated rules, got %+v", cr.Rules)
	}

	// Delete the CR: finalizer removes the cluster-scoped output
	if err := tc.client.Delete(ctx, got); err != nil {
		t.Fatalf("failed to delete JinjaTemplate: %v", err)
	}
	if !waitForGone(ctx, tc.client, types.NamespacedName{Name: crName}, &rbacv1.ClusterRole{}) {
		t.Errorf("expected ClusterRole to be deleted with the CR")
	}
	if !waitForGone(ctx, tc.client, jtKey, &jtov1.JinjaTemplate{}) {
		t.Errorf("expected JinjaTemplate to be released by the finalizer")
	}
}

func TestRawObjectNamespacedWithOwnerReference(t *testing.T) {
	ctx := context.Background()

	cfg := rbacRawObjectConfig()
	tc := setupTestManager(t, cfg)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	cfg.RawObjectAllowlist[0].Namespaces = append(cfg.RawObjectAllowlist[0].Namespaces, ns.Name)

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "raw-namespaced",
			Namespace: ns.Name,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: raw-it-role
rules: []
`,
			Output: jtov1.Output{Kind: controller.OutputKindRawObject},
		},
	}
	if err := tc.client.Create(ctx, jt); err != nil {
		t.Fatalf("failed to create JinjaTemplate: %v", err)
	}

	jtKey := types.NamespacedName{Name: jt.Name, Namespace: ns.Name}
	got, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue)
	if err != nil {
		t.Fatalf("failed to get JinjaTemplate: %v", err)
	}
	if controllerutil.ContainsFinalizer(got, controller.FinalizerRawOutputCleanup) {
		t.Errorf("namespaced raw output must not add the cleanup finalizer")
	}

	role := &rbacv1.Role{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: "raw-it-role", Namespace: ns.Name}, role); err != nil {
		t.Fatalf("expected Role in CR namespace: %v", err)
	}
	if len(role.OwnerReferences) != 1 || role.OwnerReferences[0].Name != jt.Name {
		t.Errorf("expected owner reference to the CR, got %+v", role.OwnerReferences)
	}
}

func TestRawObjectDeniedNamespace(t *testing.T) {
	ctx := context.Background()

	// Allowlist stays empty: default deny
	tc := setupTestManager(t, rbacRawObjectConfig())
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "raw-denied",
			Namespace: ns.Name,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: raw-it-denied-` + ns.Name + `
rules: []
`,
			Output: jtov1.Output{Kind: controller.OutputKindRawObject},
		},
	}
	if err := tc.client.Create(ctx, jt); err != nil {
		t.Fatalf("failed to create JinjaTemplate: %v", err)
	}

	jtKey := types.NamespacedName{Name: jt.Name, Namespace: ns.Name}
	got, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
	if err != nil {
		t.Fatalf("failed to get JinjaTemplate: %v", err)
	}

	var ready *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == controller.ConditionReady {
			ready = &got.Status.Conditions[i]
		}
	}
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != controller.ReasonRawObjectDenied {
		t.Fatalf("expected Ready=False with reason %s, got %+v", controller.ReasonRawObjectDenied, ready)
	}

	cr := &rbacv1.ClusterRole{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: "raw-it-denied-" + ns.Name}, cr); !apierrors.IsNotFound(err) {
		t.Errorf("expected no ClusterRole to be created, got err=%v", err)
	}
}
