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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/controller"
)

// The raw object integration tests run against envtest with RBAC enabled.
// The test (and thus the in-process operator) authenticates as an admin in
// system:masters, which may impersonate any ServiceAccount; the impersonated
// ServiceAccount itself is subject to normal RBAC. PriorityClass is used as
// the cluster-scoped target kind and ConfigMap as the namespaced one — both
// avoid the RBAC escalation-prevention rules that rendering Roles or
// ClusterRoles would trigger.

const rawITServiceAccount = "raw-applier"

// createRawServiceAccount creates the impersonation target in the namespace.
func createRawServiceAccount(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: rawITServiceAccount, Namespace: namespace},
	}
	if err := c.Create(context.Background(), sa); err != nil {
		t.Fatalf("failed to create ServiceAccount: %v", err)
	}
}

// grantPriorityClassRBAC grants the tenant ServiceAccount full access to
// PriorityClasses. Created as the envtest admin, so RBAC
// escalation-prevention does not apply to the grant itself. Returns a
// cleanup function: the grants are cluster-scoped and would otherwise leak
// into other tests.
func grantPriorityClassRBAC(t *testing.T, c client.Client, namespace string) func() {
	t.Helper()
	ctx := context.Background()

	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "raw-it-pc-" + namespace},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"scheduling.k8s.io"},
				Resources: []string{"priorityclasses"},
				Verbs:     []string{"get", "create", "patch", "delete"},
			},
		},
	}
	if err := c.Create(ctx, role); err != nil {
		t.Fatalf("failed to create ClusterRole: %v", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "raw-it-pc-" + namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: rawITServiceAccount, Namespace: namespace},
		},
	}
	if err := c.Create(ctx, binding); err != nil {
		t.Fatalf("failed to create ClusterRoleBinding: %v", err)
	}

	return func() {
		_ = c.Delete(ctx, binding)
		_ = c.Delete(ctx, role)
	}
}

// priorityClassTemplate renders a cluster-scoped PriorityClass manifest.
func priorityClassTemplate(name string) string {
	return `apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: ` + name + `
value: 1000
`
}

func rawObjectJinjaTemplate(name, namespace, template string) *jtov1.JinjaTemplate {
	return &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: template,
			Output: jtov1.Output{
				Kind:               controller.OutputKindRawObject,
				ServiceAccountName: rawITServiceAccount,
			},
		},
	}
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

// readyConditionOf returns the Ready condition of the CR, or nil.
func readyConditionOf(jt *jtov1.JinjaTemplate) *metav1.Condition {
	for i := range jt.Status.Conditions {
		if jt.Status.Conditions[i].Type == controller.ConditionReady {
			return &jt.Status.Conditions[i]
		}
	}
	return nil
}

func TestRawObjectClusterScopedLifecycle(t *testing.T) {
	ctx := context.Background()

	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	createRawServiceAccount(t, tc.client, ns.Name)
	defer grantPriorityClassRBAC(t, tc.client, ns.Name)()

	pcName := "raw-it-" + ns.Name

	jt := rawObjectJinjaTemplate("raw-cluster-scoped", ns.Name, priorityClassTemplate(pcName))
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
	if got.Status.LastOutput == nil || got.Status.LastOutput.APIVersion != "scheduling.k8s.io/v1" ||
		got.Status.LastOutput.Kind != "PriorityClass" || got.Status.LastOutput.Name != pcName ||
		got.Status.LastOutput.ServiceAccountName != rawITServiceAccount {
		t.Errorf("unexpected lastOutput: %+v", got.Status.LastOutput)
	}

	// Output object exists with labels, no owner reference (cluster-scoped)
	pc := &schedulingv1.PriorityClass{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); err != nil {
		t.Fatalf("expected PriorityClass to exist: %v", err)
	}
	if pc.Labels[controller.LabelManagedBy] != controller.ManagerName {
		t.Errorf("expected managed-by label, got %v", pc.Labels)
	}
	if len(pc.OwnerReferences) != 0 {
		t.Errorf("cluster-scoped output must not have owner references, got %v", pc.OwnerReferences)
	}
	if pc.Value != 1000 {
		t.Errorf("unexpected value: %d", pc.Value)
	}

	// Update the template: object is re-applied via SSA
	if err := tc.client.Get(ctx, jtKey, got); err != nil {
		t.Fatalf("failed to re-get JinjaTemplate: %v", err)
	}
	got.Spec.Template = `apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: ` + pcName + `
value: 1000
description: updated by test
`
	if err := tc.client.Update(ctx, got); err != nil {
		t.Fatalf("failed to update JinjaTemplate: %v", err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); err == nil &&
			pc.Description == "updated by test" {
			break
		}
		time.Sleep(interval)
	}
	if pc.Description != "updated by test" {
		t.Errorf("expected updated description, got %q", pc.Description)
	}

	// Delete the CR: finalizer removes the cluster-scoped output via the
	// impersonated ServiceAccount
	if err := tc.client.Delete(ctx, got); err != nil {
		t.Fatalf("failed to delete JinjaTemplate: %v", err)
	}
	if !waitForGone(ctx, tc.client, types.NamespacedName{Name: pcName}, &schedulingv1.PriorityClass{}) {
		t.Errorf("expected PriorityClass to be deleted with the CR")
	}
	if !waitForGone(ctx, tc.client, jtKey, &jtov1.JinjaTemplate{}) {
		t.Errorf("expected JinjaTemplate to be released by the finalizer")
	}
}

func TestRawObjectNamespacedWithOwnerReference(t *testing.T) {
	ctx := context.Background()

	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	createRawServiceAccount(t, tc.client, ns.Name)

	// Namespaced grant for the namespaced target kind (ConfigMap)
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "raw-it-cm", Namespace: ns.Name},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "create", "patch", "delete"},
			},
		},
	}
	if err := tc.client.Create(ctx, role); err != nil {
		t.Fatalf("failed to create Role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "raw-it-cm", Namespace: ns.Name},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: rawITServiceAccount, Namespace: ns.Name},
		},
	}
	if err := tc.client.Create(ctx, binding); err != nil {
		t.Fatalf("failed to create RoleBinding: %v", err)
	}

	jt := rawObjectJinjaTemplate("raw-namespaced", ns.Name, `apiVersion: v1
kind: ConfigMap
metadata:
  name: raw-it-cm
data:
  hello: world
`)
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

	cm := &corev1.ConfigMap{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: "raw-it-cm", Namespace: ns.Name}, cm); err != nil {
		t.Fatalf("expected ConfigMap in CR namespace: %v", err)
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Name != jt.Name {
		t.Errorf("expected owner reference to the CR, got %+v", cm.OwnerReferences)
	}
	if cm.Data["hello"] != "world" {
		t.Errorf("unexpected data: %+v", cm.Data)
	}
}

func TestRawObjectForbiddenThenGranted(t *testing.T) {
	ctx := context.Background()

	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	createRawServiceAccount(t, tc.client, ns.Name)
	// Deliberately no RBAC grant yet

	pcName := "raw-it-forbidden-" + ns.Name
	jt := rawObjectJinjaTemplate("raw-forbidden", ns.Name, priorityClassTemplate(pcName))
	if err := tc.client.Create(ctx, jt); err != nil {
		t.Fatalf("failed to create JinjaTemplate: %v", err)
	}

	jtKey := types.NamespacedName{Name: jt.Name, Namespace: ns.Name}
	got, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
	if err != nil {
		t.Fatalf("failed to get JinjaTemplate: %v", err)
	}

	ready := readyConditionOf(got)
	if ready == nil || ready.Reason != controller.ReasonOutputForbidden {
		t.Fatalf("expected Ready=False with reason %s, got %+v", controller.ReasonOutputForbidden, ready)
	}
	if !strings.Contains(ready.Message, "system:serviceaccount:"+ns.Name+":"+rawITServiceAccount) {
		t.Errorf("expected the impersonated identity in the message, got %q", ready.Message)
	}
	if !strings.Contains(ready.Message, "kubectl auth can-i create priorityclasses.scheduling.k8s.io") {
		t.Errorf("expected a can-i remediation hint in the message, got %q", ready.Message)
	}

	pc := &schedulingv1.PriorityClass{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); !apierrors.IsNotFound(err) {
		t.Errorf("expected no PriorityClass to be created, got err=%v", err)
	}

	// Grant RBAC as admin: the backoff-requeue must turn the CR green
	// without any further change to the CR.
	defer grantPriorityClassRBAC(t, tc.client, ns.Name)()

	if _, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue); err != nil {
		t.Fatalf("expected CR to turn Ready after the RBAC grant: %v", err)
	}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); err != nil {
		t.Errorf("expected PriorityClass to exist after the grant: %v", err)
	}

	// Cleanup: drop the finalizer path cleanly by deleting the CR while the
	// grant is still in place.
	got = &jtov1.JinjaTemplate{}
	if err := tc.client.Get(ctx, jtKey, got); err == nil {
		_ = tc.client.Delete(ctx, got)
		waitForGone(ctx, tc.client, jtKey, &jtov1.JinjaTemplate{})
	}
}

func TestRawObjectServiceAccountMissingThenCreated(t *testing.T) {
	ctx := context.Background()

	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)
	// Grant RBAC up front — only the ServiceAccount itself is missing.
	defer grantPriorityClassRBAC(t, tc.client, ns.Name)()

	pcName := "raw-it-nosa-" + ns.Name
	jt := rawObjectJinjaTemplate("raw-no-sa", ns.Name, priorityClassTemplate(pcName))
	if err := tc.client.Create(ctx, jt); err != nil {
		t.Fatalf("failed to create JinjaTemplate: %v", err)
	}

	jtKey := types.NamespacedName{Name: jt.Name, Namespace: ns.Name}
	got, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
	if err != nil {
		t.Fatalf("failed to get JinjaTemplate: %v", err)
	}

	ready := readyConditionOf(got)
	if ready == nil || ready.Reason != controller.ReasonServiceAccountNotFound {
		t.Fatalf("expected Ready=False with reason %s, got %+v", controller.ReasonServiceAccountNotFound, ready)
	}

	pc := &schedulingv1.PriorityClass{}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); !apierrors.IsNotFound(err) {
		t.Errorf("expected no PriorityClass to be created, got err=%v", err)
	}

	// Creating the ServiceAccount must turn the CR green via backoff-requeue.
	createRawServiceAccount(t, tc.client, ns.Name)

	if _, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue); err != nil {
		t.Fatalf("expected CR to turn Ready after ServiceAccount creation: %v", err)
	}
	if err := tc.client.Get(ctx, types.NamespacedName{Name: pcName}, pc); err != nil {
		t.Errorf("expected PriorityClass to exist after ServiceAccount creation: %v", err)
	}

	// Cleanup while identity and grant still exist.
	got = &jtov1.JinjaTemplate{}
	if err := tc.client.Get(ctx, jtKey, got); err == nil {
		_ = tc.client.Delete(ctx, got)
		waitForGone(ctx, tc.client, jtKey, &jtov1.JinjaTemplate{})
	}
}
