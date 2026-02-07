//go:build e2e
// +build e2e

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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// testNamespace is the namespace used for E2E tests.
	testNamespace = "default"

	// pollInterval is the interval for polling operations.
	pollInterval = 1 * time.Second

	// pollTimeout is the timeout for polling operations.
	pollTimeout = 60 * time.Second
)

var (
	clientset     *kubernetes.Clientset
	dynamicClient dynamic.Interface

	jinjaTemplateGVR = schema.GroupVersionResource{
		Group:    "jto.gtrfc.com",
		Version:  "v1",
		Resource: "jinjatemplates",
	}
)

// testResourceNames tracks all resource names created during tests for cleanup.
var testJinjaTemplateNames = []string{
	"e2e-basic-configmap",
	"e2e-basic-secret",
	"e2e-source-configmap-direct",
	"e2e-source-secret-direct",
	"e2e-source-cm-labelselector",
	"e2e-source-secret-labelselector",
	"e2e-template-from-configmap",
	"e2e-output-name-default",
	"e2e-custom-output-name",
	"e2e-ownerref-enabled",
	"e2e-ownerref-disabled",
	"e2e-rerender-on-change",
	"e2e-multiple-sources",
	"e2e-jinja-loop-filters",
	"e2e-missing-source",
	"e2e-bad-template-syntax",
	"e2e-labelselector-dynamic",
}

// testHelperResourceNames tracks helper ConfigMaps/Secrets created for tests.
var testHelperConfigMapNames = []string{
	"e2e-src-cm-db-config",
	"e2e-src-cm-template",
	"e2e-src-cm-rerender",
	"e2e-src-cm-multi-a",
	"e2e-src-cm-multi-b",
	"e2e-endpoint-alpha",
	"e2e-endpoint-beta",
	"e2e-endpoint-dynamic-gamma",
	"e2e-src-cm-loop",
}

var testHelperSecretNames = []string{
	"e2e-src-secret-creds",
	"e2e-labeled-secret-one",
	"e2e-labeled-secret-two",
}

// testOutputResourceNames tracks operator-generated output resources.
var testOutputConfigMapNames = []string{
	"e2e-basic-configmap",
	"e2e-source-configmap-direct",
	"e2e-source-cm-labelselector",
	"e2e-template-from-configmap",
	"e2e-output-name-default",
	"e2e-custom-output-target",
	"e2e-ownerref-enabled",
	"e2e-ownerref-disabled",
	"e2e-rerender-on-change",
	"e2e-multiple-sources",
	"e2e-jinja-loop-filters",
	"e2e-labelselector-dynamic",
}

var testOutputSecretNames = []string{
	"e2e-basic-secret",
	"e2e-source-secret-direct",
	"e2e-source-secret-labelselector",
}

func TestMain(m *testing.M) {
	// Build kubeconfig
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(fmt.Sprintf("Failed to build kubeconfig: %v", err))
	}

	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("Failed to create kubernetes clientset: %v", err))
	}

	dynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		panic(fmt.Sprintf("Failed to create dynamic client: %v", err))
	}

	// Cleanup any leftover test resources before running tests
	cleanupAllTestResources()

	code := m.Run()

	// Cleanup test resources after all tests
	cleanupAllTestResources()

	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Cleanup helpers
// ---------------------------------------------------------------------------

func cleanupAllTestResources() {
	ctx := context.Background()

	for _, name := range testJinjaTemplateNames {
		_ = dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	// Wait a bit for owner-reference cascading deletions
	time.Sleep(2 * time.Second)

	for _, name := range testOutputConfigMapNames {
		_ = clientset.CoreV1().ConfigMaps(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	for _, name := range testOutputSecretNames {
		_ = clientset.CoreV1().Secrets(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	for _, name := range testHelperConfigMapNames {
		_ = clientset.CoreV1().ConfigMaps(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
	for _, name := range testHelperSecretNames {
		_ = clientset.CoreV1().Secrets(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	}
}

func cleanupJinjaTemplate(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	err := dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		t.Logf("Warning: failed to delete JinjaTemplate %s: %v", name, err)
	}
}

func cleanupConfigMap(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	err := clientset.CoreV1().ConfigMaps(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		t.Logf("Warning: failed to delete ConfigMap %s: %v", name, err)
	}
}

func cleanupSecret(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	err := clientset.CoreV1().Secrets(testNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		t.Logf("Warning: failed to delete Secret %s: %v", name, err)
	}
}

// ---------------------------------------------------------------------------
// JinjaTemplate CR builder helpers
// ---------------------------------------------------------------------------

func newJinjaTemplateObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "jto.gtrfc.com/v1",
			"kind":       "JinjaTemplate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": testNamespace,
			},
			"spec": map[string]interface{}{},
		},
	}
}

func setInlineTemplate(obj *unstructured.Unstructured, tmpl string) {
	spec := obj.Object["spec"].(map[string]interface{})
	spec["template"] = tmpl
}

func setTemplateFrom(obj *unstructured.Unstructured, cmName, key string) {
	spec := obj.Object["spec"].(map[string]interface{})
	spec["templateFrom"] = map[string]interface{}{
		"configMapRef": map[string]interface{}{
			"name": cmName,
			"key":  key,
		},
	}
}

func setOutput(obj *unstructured.Unstructured, kind, name string) {
	spec := obj.Object["spec"].(map[string]interface{})
	output := map[string]interface{}{
		"kind": kind,
	}
	if name != "" {
		output["name"] = name
	}
	spec["output"] = output
}

func setOwnerReference(obj *unstructured.Unstructured, val bool) {
	spec := obj.Object["spec"].(map[string]interface{})
	spec["setOwnerReference"] = val
}

func addSourceConfigMapDirect(obj *unstructured.Unstructured, varName, cmName, key string) {
	spec := obj.Object["spec"].(map[string]interface{})
	srcs, _ := spec["sources"].([]interface{})
	srcs = append(srcs, map[string]interface{}{
		"name": varName,
		"configMap": map[string]interface{}{
			"name": cmName,
			"key":  key,
		},
	})
	spec["sources"] = srcs
}

func addSourceSecretDirect(obj *unstructured.Unstructured, varName, secretName, key string) {
	spec := obj.Object["spec"].(map[string]interface{})
	srcs, _ := spec["sources"].([]interface{})
	srcs = append(srcs, map[string]interface{}{
		"name": varName,
		"secret": map[string]interface{}{
			"name": secretName,
			"key":  key,
		},
	})
	spec["sources"] = srcs
}

func addSourceConfigMapLabelSelector(obj *unstructured.Unstructured, varName string, matchLabels map[string]interface{}) {
	spec := obj.Object["spec"].(map[string]interface{})
	srcs, _ := spec["sources"].([]interface{})
	srcs = append(srcs, map[string]interface{}{
		"name": varName,
		"configMap": map[string]interface{}{
			"labelSelector": map[string]interface{}{
				"matchLabels": matchLabels,
			},
		},
	})
	spec["sources"] = srcs
}

func addSourceSecretLabelSelector(obj *unstructured.Unstructured, varName string, matchLabels map[string]interface{}) {
	spec := obj.Object["spec"].(map[string]interface{})
	srcs, _ := spec["sources"].([]interface{})
	srcs = append(srcs, map[string]interface{}{
		"name": varName,
		"secret": map[string]interface{}{
			"labelSelector": map[string]interface{}{
				"matchLabels": matchLabels,
			},
		},
	})
	spec["sources"] = srcs
}

func createJinjaTemplate(t *testing.T, obj *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	ctx := context.Background()
	created, err := dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create JinjaTemplate %s: %v", obj.GetName(), err)
	}
	return created
}

// ---------------------------------------------------------------------------
// Wait helpers
// ---------------------------------------------------------------------------

func waitForJinjaTemplateReady(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		obj, err := dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return isConditionTrue(obj, "Ready"), nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for JinjaTemplate %s to become Ready: %v", name, err)
	}
}

func waitForJinjaTemplateNotReady(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		obj, err := dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return isConditionFalse(obj, "Ready"), nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for JinjaTemplate %s to have Ready=False: %v", name, err)
	}
}

func waitForConfigMap(t *testing.T, name string) *corev1.ConfigMap {
	t.Helper()
	ctx := context.Background()
	var result *corev1.ConfigMap
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		cm, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, nil
		}
		result = cm
		return true, nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for ConfigMap %s: %v", name, err)
	}
	return result
}

func waitForSecret(t *testing.T, name string) *corev1.Secret {
	t.Helper()
	ctx := context.Background()
	var result *corev1.Secret
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		s, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, nil
		}
		result = s
		return true, nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for Secret %s: %v", name, err)
	}
	return result
}

func waitForConfigMapContent(t *testing.T, name, expectedContent string) *corev1.ConfigMap {
	t.Helper()
	ctx := context.Background()
	var result *corev1.ConfigMap
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		cm, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if cm.Data["content"] == expectedContent {
			result = cm
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for ConfigMap %s to have expected content: %v", name, err)
	}
	return result
}

func waitForSecretContent(t *testing.T, name, expectedContent string) *corev1.Secret {
	t.Helper()
	ctx := context.Background()
	var result *corev1.Secret
	err := wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		s, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if string(s.Data["content"]) == expectedContent {
			result = s
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for Secret %s to have expected content: %v", name, err)
	}
	return result
}

// isConditionTrue checks if the given condition type has Status=True on the unstructured object.
func isConditionTrue(obj *unstructured.Unstructured, condType string) bool {
	return conditionHasStatus(obj, condType, "True")
}

// isConditionFalse checks if the given condition type has Status=False on the unstructured object.
func isConditionFalse(obj *unstructured.Unstructured, condType string) bool {
	return conditionHasStatus(obj, condType, "False")
}

func conditionHasStatus(obj *unstructured.Unstructured, condType, status string) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == condType && cond["status"] == status {
			return true
		}
	}
	return false
}

func getConditionMessage(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	obj, err := dynamicClient.Resource(jinjaTemplateGVR).Namespace(testNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get JinjaTemplate %s: %v", name, err)
	}
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return ""
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" {
			msg, _ := cond["message"].(string)
			return msg
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Test: Basic inline template → ConfigMap output
// ---------------------------------------------------------------------------

func TestBasicInlineTemplateToConfigMap(t *testing.T) {
	const name = "e2e-basic-configmap"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "HELLO=world")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)

	if cm.Data["content"] != "HELLO=world" {
		t.Errorf("Expected content 'HELLO=world', got %q", cm.Data["content"])
	}

	// Verify managed-by labels
	if cm.Labels["jto.gtrfc.com/managed-by"] != "jinja-template-operator" {
		t.Errorf("Expected managed-by label, got %v", cm.Labels)
	}
	if cm.Labels["jto.gtrfc.com/jinja-template"] != name {
		t.Errorf("Expected jinja-template label %s, got %v", name, cm.Labels)
	}

	t.Log("Basic inline template → ConfigMap test passed")
}

// ---------------------------------------------------------------------------
// Test: Basic inline template → Secret output
// ---------------------------------------------------------------------------

func TestBasicInlineTemplateToSecret(t *testing.T) {
	const name = "e2e-basic-secret"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupSecret(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "SECRET_KEY=super-secret-value")
	setOutput(jt, "Secret", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	s := waitForSecret(t, name)

	if string(s.Data["content"]) != "SECRET_KEY=super-secret-value" {
		t.Errorf("Expected content 'SECRET_KEY=super-secret-value', got %q", string(s.Data["content"]))
	}

	// Verify managed-by labels
	if s.Labels["jto.gtrfc.com/managed-by"] != "jinja-template-operator" {
		t.Errorf("Expected managed-by label, got %v", s.Labels)
	}

	t.Log("Basic inline template → Secret test passed")
}

// ---------------------------------------------------------------------------
// Test: Source from ConfigMap (direct name+key reference)
// ---------------------------------------------------------------------------

func TestSourceFromConfigMapDirectRef(t *testing.T) {
	const name = "e2e-source-configmap-direct"
	const srcCM = "e2e-src-cm-db-config"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, srcCM)

	ctx := context.Background()

	// Create the source ConfigMap
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCM, Namespace: testNamespace},
		Data:       map[string]string{"host": "db.example.com"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create source ConfigMap: %v", err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "db_host", srcCM, "host")
	setInlineTemplate(jt, "DATABASE_HOST={{ db_host }}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMapContent(t, name, "DATABASE_HOST=db.example.com")
	t.Logf("Output content: %s", cm.Data["content"])
	t.Log("Source from ConfigMap direct reference test passed")
}

// ---------------------------------------------------------------------------
// Test: Source from Secret (direct name+key reference)
// ---------------------------------------------------------------------------

func TestSourceFromSecretDirectRef(t *testing.T) {
	const name = "e2e-source-secret-direct"
	const srcSecret = "e2e-src-secret-creds"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupSecret(t, name)
	defer cleanupSecret(t, srcSecret)

	ctx := context.Background()

	// Create the source Secret
	_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: srcSecret, Namespace: testNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"password": []byte("s3cret!")},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create source Secret: %v", err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceSecretDirect(jt, "db_password", srcSecret, "password")
	setInlineTemplate(jt, "DATABASE_PASSWORD={{ db_password }}")
	setOutput(jt, "Secret", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	s := waitForSecretContent(t, name, "DATABASE_PASSWORD=s3cret!")
	t.Logf("Output content: %s", string(s.Data["content"]))
	t.Log("Source from Secret direct reference test passed")
}

// ---------------------------------------------------------------------------
// Test: Source from ConfigMap via label selector
// ---------------------------------------------------------------------------

func TestSourceFromConfigMapLabelSelector(t *testing.T) {
	const name = "e2e-source-cm-labelselector"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, "e2e-endpoint-alpha")
	defer cleanupConfigMap(t, "e2e-endpoint-beta")

	ctx := context.Background()

	// Create labeled ConfigMaps
	for _, ep := range []struct {
		name string
		data map[string]string
	}{
		{"e2e-endpoint-alpha", map[string]string{"url": "https://alpha.example.com"}},
		{"e2e-endpoint-beta", map[string]string{"url": "https://beta.example.com"}},
	} {
		_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ep.name,
				Namespace: testNamespace,
				Labels:    map[string]string{"e2e-type": "endpoint"},
			},
			Data: ep.data,
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create ConfigMap %s: %v", ep.name, err)
		}
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapLabelSelector(jt, "endpoints", map[string]interface{}{"e2e-type": "endpoint"})
	setInlineTemplate(jt, "{% for ep in endpoints %}{{ ep.name }}: {{ ep.data.url }}\n{% endfor %}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)

	content := cm.Data["content"]
	if !strings.Contains(content, "e2e-endpoint-alpha") || !strings.Contains(content, "e2e-endpoint-beta") {
		t.Errorf("Expected content to contain both endpoints, got:\n%s", content)
	}
	if !strings.Contains(content, "https://alpha.example.com") || !strings.Contains(content, "https://beta.example.com") {
		t.Errorf("Expected content to contain both URLs, got:\n%s", content)
	}

	t.Logf("Output content:\n%s", content)
	t.Log("Source from ConfigMap label selector test passed")
}

// ---------------------------------------------------------------------------
// Test: Source from Secret via label selector
// ---------------------------------------------------------------------------

func TestSourceFromSecretLabelSelector(t *testing.T) {
	const name = "e2e-source-secret-labelselector"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupSecret(t, name)
	defer cleanupSecret(t, "e2e-labeled-secret-one")
	defer cleanupSecret(t, "e2e-labeled-secret-two")

	ctx := context.Background()

	for _, s := range []struct {
		name string
		data map[string][]byte
	}{
		{"e2e-labeled-secret-one", map[string][]byte{"token": []byte("tok-111")}},
		{"e2e-labeled-secret-two", map[string][]byte{"token": []byte("tok-222")}},
	} {
		_, err := clientset.CoreV1().Secrets(testNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name,
				Namespace: testNamespace,
				Labels:    map[string]string{"e2e-type": "api-token"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: s.data,
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create Secret %s: %v", s.name, err)
		}
	}

	jt := newJinjaTemplateObj(name)
	addSourceSecretLabelSelector(jt, "tokens", map[string]interface{}{"e2e-type": "api-token"})
	setInlineTemplate(jt, "{% for s in tokens %}{{ s.name }}={{ s.data.token }}\n{% endfor %}")
	setOutput(jt, "Secret", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	sec := waitForSecret(t, name)
	content := string(sec.Data["content"])

	if !strings.Contains(content, "e2e-labeled-secret-one") || !strings.Contains(content, "e2e-labeled-secret-two") {
		t.Errorf("Expected content to contain both secrets, got:\n%s", content)
	}

	t.Logf("Output content:\n%s", content)
	t.Log("Source from Secret label selector test passed")
}

// ---------------------------------------------------------------------------
// Test: Template loaded from ConfigMap (templateFrom)
// ---------------------------------------------------------------------------

func TestTemplateFromConfigMap(t *testing.T) {
	const name = "e2e-template-from-configmap"
	const tplCM = "e2e-src-cm-template"
	const srcCM = "e2e-src-cm-db-config"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, tplCM)
	// srcCM may already be created by another test; ignore errors
	defer cleanupConfigMap(t, srcCM)

	ctx := context.Background()

	// Ensure the source ConfigMap exists
	_, _ = clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCM, Namespace: testNamespace},
		Data:       map[string]string{"host": "db.example.com"},
	}, metav1.CreateOptions{})

	// Create the template ConfigMap
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: tplCM, Namespace: testNamespace},
		Data:       map[string]string{"tpl": "DB_HOST={{ db_host }}"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create template ConfigMap: %v", err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "db_host", srcCM, "host")
	setTemplateFrom(jt, tplCM, "tpl")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMapContent(t, name, "DB_HOST=db.example.com")
	t.Logf("Output content: %s", cm.Data["content"])
	t.Log("TemplateFrom ConfigMap test passed")
}

// ---------------------------------------------------------------------------
// Test: Output name defaults to CR name
// ---------------------------------------------------------------------------

func TestOutputNameDefaulting(t *testing.T) {
	const name = "e2e-output-name-default"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "DEFAULT_OUTPUT=true")
	// Output without explicit name → should default to CR name
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)
	if cm.Data["content"] != "DEFAULT_OUTPUT=true" {
		t.Errorf("Expected content 'DEFAULT_OUTPUT=true', got %q", cm.Data["content"])
	}

	t.Log("Output name defaulting test passed")
}

// ---------------------------------------------------------------------------
// Test: Custom output name
// ---------------------------------------------------------------------------

func TestCustomOutputName(t *testing.T) {
	const name = "e2e-custom-output-name"
	const outputName = "e2e-custom-output-target"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, outputName)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "CUSTOM_OUTPUT=yes")
	setOutput(jt, "ConfigMap", outputName)

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, outputName)
	if cm.Data["content"] != "CUSTOM_OUTPUT=yes" {
		t.Errorf("Expected content 'CUSTOM_OUTPUT=yes', got %q", cm.Data["content"])
	}

	// Verify that no ConfigMap with the CR name exists
	ctx := context.Background()
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(ctx, name, metav1.GetOptions{})
	if !errors.IsNotFound(err) {
		t.Errorf("Expected no ConfigMap with CR name %s, but found one", name)
	}

	t.Log("Custom output name test passed")
}

// ---------------------------------------------------------------------------
// Test: OwnerReference enabled → output has owner ref
// ---------------------------------------------------------------------------

func TestOwnerReferenceEnabled(t *testing.T) {
	const name = "e2e-ownerref-enabled"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "OWNER_REF=enabled")
	setOutput(jt, "ConfigMap", "")
	setOwnerReference(jt, true)

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)

	// Check owner references
	if len(cm.OwnerReferences) == 0 {
		t.Fatal("Expected owner reference on ConfigMap, but found none")
	}

	found := false
	for _, ref := range cm.OwnerReferences {
		if ref.Kind == "JinjaTemplate" && ref.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected OwnerReference pointing to JinjaTemplate %s, got %v", name, cm.OwnerReferences)
	}

	t.Log("OwnerReference enabled test passed")
}

// ---------------------------------------------------------------------------
// Test: OwnerReference disabled → output has NO owner ref
// ---------------------------------------------------------------------------

func TestOwnerReferenceDisabled(t *testing.T) {
	const name = "e2e-ownerref-disabled"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "OWNER_REF=disabled")
	setOutput(jt, "ConfigMap", "")
	setOwnerReference(jt, false)

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)

	// Check that no owner references exist
	for _, ref := range cm.OwnerReferences {
		if ref.Kind == "JinjaTemplate" && ref.Name == name {
			t.Fatalf("Expected no OwnerReference for JinjaTemplate, but found one: %v", ref)
		}
	}

	t.Log("OwnerReference disabled test passed")
}

// ---------------------------------------------------------------------------
// Test: Re-render on source change
// ---------------------------------------------------------------------------

func TestReRenderOnSourceChange(t *testing.T) {
	const name = "e2e-rerender-on-change"
	const srcCM = "e2e-src-cm-rerender"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, srcCM)

	ctx := context.Background()

	// Create the source ConfigMap with initial value
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCM, Namespace: testNamespace},
		Data:       map[string]string{"version": "v1"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create source ConfigMap: %v", err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "app_version", srcCM, "version")
	setInlineTemplate(jt, "APP_VERSION={{ app_version }}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	// Verify initial content
	waitForConfigMapContent(t, name, "APP_VERSION=v1")
	t.Log("Initial content verified: APP_VERSION=v1")

	// Update the source ConfigMap
	srcObj, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(ctx, srcCM, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get source ConfigMap: %v", err)
	}
	srcObj.Data["version"] = "v2"
	_, err = clientset.CoreV1().ConfigMaps(testNamespace).Update(ctx, srcObj, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update source ConfigMap: %v", err)
	}

	// Wait for re-rendered content
	waitForConfigMapContent(t, name, "APP_VERSION=v2")

	t.Log("Re-render on source change test passed")
}

// ---------------------------------------------------------------------------
// Test: Multiple sources combined
// ---------------------------------------------------------------------------

func TestMultipleSources(t *testing.T) {
	const name = "e2e-multiple-sources"
	const srcCMA = "e2e-src-cm-multi-a"
	const srcCMB = "e2e-src-cm-multi-b"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, srcCMA)
	defer cleanupConfigMap(t, srcCMB)

	ctx := context.Background()

	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCMA, Namespace: testNamespace},
		Data:       map[string]string{"host": "redis.local"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap %s: %v", srcCMA, err)
	}

	_, err = clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCMB, Namespace: testNamespace},
		Data:       map[string]string{"port": "6379"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create ConfigMap %s: %v", srcCMB, err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "redis_host", srcCMA, "host")
	addSourceConfigMapDirect(jt, "redis_port", srcCMB, "port")
	setInlineTemplate(jt, "REDIS_URL={{ redis_host }}:{{ redis_port }}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMapContent(t, name, "REDIS_URL=redis.local:6379")
	t.Logf("Output content: %s", cm.Data["content"])
	t.Log("Multiple sources test passed")
}

// ---------------------------------------------------------------------------
// Test: Jinja loop and filters
// ---------------------------------------------------------------------------

func TestJinjaLoopAndFilters(t *testing.T) {
	const name = "e2e-jinja-loop-filters"
	const srcCM = "e2e-src-cm-loop"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, srcCM)

	ctx := context.Background()

	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: srcCM, Namespace: testNamespace},
		Data:       map[string]string{"name": "jinja-operator"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create source ConfigMap: %v", err)
	}

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "app_name", srcCM, "name")
	setInlineTemplate(jt, "APP={{ app_name | upper }}\n{% for i in range(3) %}ITEM_{{ i }}=value{{ i }}\n{% endfor %}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	cm := waitForConfigMap(t, name)
	content := cm.Data["content"]

	if !strings.Contains(content, "APP=JINJA-OPERATOR") {
		t.Errorf("Expected upper-cased app name, got:\n%s", content)
	}
	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("ITEM_%d=value%d", i, i)
		if !strings.Contains(content, expected) {
			t.Errorf("Expected %q in content, got:\n%s", expected, content)
		}
	}

	t.Logf("Output content:\n%s", content)
	t.Log("Jinja loop and filters test passed")
}

// ---------------------------------------------------------------------------
// Test: Missing source → Ready=False
// ---------------------------------------------------------------------------

func TestMissingSourceHandled(t *testing.T) {
	const name = "e2e-missing-source"
	defer cleanupJinjaTemplate(t, name)

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapDirect(jt, "nonexistent", "e2e-does-not-exist", "key")
	setInlineTemplate(jt, "VALUE={{ nonexistent }}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateNotReady(t, name)

	msg := getConditionMessage(t, name)
	t.Logf("Condition message: %s", msg)

	if msg == "" {
		t.Error("Expected a non-empty condition message on Ready=False")
	}

	t.Log("Missing source error handling test passed")
}

// ---------------------------------------------------------------------------
// Test: Bad template syntax → Ready=False
// ---------------------------------------------------------------------------

func TestBadTemplateSyntax(t *testing.T) {
	const name = "e2e-bad-template-syntax"
	defer cleanupJinjaTemplate(t, name)

	jt := newJinjaTemplateObj(name)
	setInlineTemplate(jt, "{% if true %}unterminated")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateNotReady(t, name)

	msg := getConditionMessage(t, name)
	t.Logf("Condition message: %s", msg)

	if msg == "" {
		t.Error("Expected a non-empty condition message for bad template syntax")
	}

	t.Log("Bad template syntax error handling test passed")
}

// ---------------------------------------------------------------------------
// Test: Dynamic label selector — new ConfigMap matching selector triggers re-render
// ---------------------------------------------------------------------------

func TestLabelSelectorDynamicMatch(t *testing.T) {
	const name = "e2e-labelselector-dynamic"
	defer cleanupJinjaTemplate(t, name)
	defer cleanupConfigMap(t, name)
	defer cleanupConfigMap(t, "e2e-endpoint-alpha")
	defer cleanupConfigMap(t, "e2e-endpoint-beta")
	defer cleanupConfigMap(t, "e2e-endpoint-dynamic-gamma")

	ctx := context.Background()

	// Create initial labeled ConfigMap
	_, _ = clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-endpoint-alpha",
			Namespace: testNamespace,
			Labels:    map[string]string{"e2e-dynamic": "endpoint"},
		},
		Data: map[string]string{"url": "https://alpha.example.com"},
	}, metav1.CreateOptions{})

	jt := newJinjaTemplateObj(name)
	addSourceConfigMapLabelSelector(jt, "endpoints", map[string]interface{}{"e2e-dynamic": "endpoint"})
	setInlineTemplate(jt, "{% for ep in endpoints %}{{ ep.name }}\n{% endfor %}")
	setOutput(jt, "ConfigMap", "")

	createJinjaTemplate(t, jt)
	waitForJinjaTemplateReady(t, name)

	// Verify initial content
	cm := waitForConfigMap(t, name)
	if !strings.Contains(cm.Data["content"], "e2e-endpoint-alpha") {
		t.Fatalf("Expected initial content to contain e2e-endpoint-alpha, got:\n%s", cm.Data["content"])
	}
	t.Logf("Initial content:\n%s", cm.Data["content"])

	// Add a new ConfigMap matching the label selector
	_, err := clientset.CoreV1().ConfigMaps(testNamespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-endpoint-dynamic-gamma",
			Namespace: testNamespace,
			Labels:    map[string]string{"e2e-dynamic": "endpoint"},
		},
		Data: map[string]string{"url": "https://gamma.example.com"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create dynamic ConfigMap: %v", err)
	}

	// Wait for the output to include the new ConfigMap
	err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		cm, err := clientset.CoreV1().ConfigMaps(testNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return strings.Contains(cm.Data["content"], "e2e-endpoint-dynamic-gamma"), nil
	})
	if err != nil {
		t.Fatalf("Timeout waiting for dynamic label selector re-render: %v", err)
	}

	t.Log("Label selector dynamic match test passed")
}
