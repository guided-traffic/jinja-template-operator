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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/controller"
)

const (
	// Test timeouts
	timeout  = 15 * time.Second
	interval = 200 * time.Millisecond
)

// waitForOutputConfigMap waits for a ConfigMap to be created with the expected managed-by label.
func waitForOutputConfigMap(ctx context.Context, c client.Client, key types.NamespacedName) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, &cm); err != nil {
			time.Sleep(interval)
			continue
		}

		if cm.Labels[controller.LabelManagedBy] == controller.ManagerName {
			return &cm, nil
		}

		time.Sleep(interval)
	}

	// Return whatever we have
	if err := c.Get(ctx, key, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

// waitForOutputSecret waits for a Secret to be created with the expected managed-by label.
func waitForOutputSecret(ctx context.Context, c client.Client, key types.NamespacedName) (*corev1.Secret, error) {
	var secret corev1.Secret
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, &secret); err != nil {
			time.Sleep(interval)
			continue
		}

		if secret.Labels[controller.LabelManagedBy] == controller.ManagerName {
			return &secret, nil
		}

		time.Sleep(interval)
	}

	// Return whatever we have
	if err := c.Get(ctx, key, &secret); err != nil {
		return nil, err
	}
	return &secret, nil
}

// waitForCondition waits for a JinjaTemplate to have the given condition status.
func waitForCondition(ctx context.Context, c client.Client, key types.NamespacedName, condType string, status metav1.ConditionStatus) (*jtov1.JinjaTemplate, error) {
	var jt jtov1.JinjaTemplate
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, &jt); err != nil {
			time.Sleep(interval)
			continue
		}

		for _, cond := range jt.Status.Conditions {
			if cond.Type == condType && cond.Status == status {
				return &jt, nil
			}
		}

		time.Sleep(interval)
	}

	// Return whatever we have
	if err := c.Get(ctx, key, &jt); err != nil {
		return nil, err
	}
	return &jt, nil
}

// waitForConfigMapContent waits until a ConfigMap exists and its "content" key matches the expected string.
func waitForConfigMapContent(ctx context.Context, c client.Client, key types.NamespacedName, expected string) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if err := c.Get(ctx, key, &cm); err != nil {
			time.Sleep(interval)
			continue
		}

		if cm.Data["content"] == expected {
			return &cm, nil
		}

		time.Sleep(interval)
	}

	if err := c.Get(ctx, key, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

// TestBasicReconciliation tests the core reconciliation flow: create JinjaTemplate → output ConfigMap.
func TestBasicReconciliation(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("InlineTemplateToConfigMap", func(t *testing.T) {
		// Create source ConfigMap
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "db-config",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"host": "db.example.com",
				"port": "5432",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source ConfigMap: %v", err)
		}

		// Create JinjaTemplate CR
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "app-config",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "db_host",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "db-config",
							Key:  "host",
						},
					},
					{
						Name: "db_port",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "db-config",
							Key:  "port",
						},
					},
				},
				Template: "DATABASE_HOST={{ db_host }}\nDATABASE_PORT={{ db_port }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for output ConfigMap
		outputKey := types.NamespacedName{Name: "app-config", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		expected := "DATABASE_HOST=db.example.com\nDATABASE_PORT=5432"
		if outputCM.Data["content"] != expected {
			t.Errorf("unexpected output content:\ngot:  %q\nwant: %q", outputCM.Data["content"], expected)
		}

		// Verify labels
		if outputCM.Labels[controller.LabelManagedBy] != controller.ManagerName {
			t.Errorf("expected managed-by label %q, got %q", controller.ManagerName, outputCM.Labels[controller.LabelManagedBy])
		}
		if outputCM.Labels[controller.LabelJinjaTemplate] != "app-config" {
			t.Errorf("expected jinja-template label %q, got %q", "app-config", outputCM.Labels[controller.LabelJinjaTemplate])
		}

		// Verify Ready condition on CR
		jtKey := types.NamespacedName{Name: "app-config", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionTrue {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=True condition on JinjaTemplate")
		}
	})

	t.Run("InlineTemplateToSecret", func(t *testing.T) {
		// Create source Secret
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "db-creds",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"password": []byte("s3cret123"),
			},
		}
		if err := tc.client.Create(ctx, sourceSecret); err != nil {
			t.Fatalf("failed to create source Secret: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret-output",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "password",
						Secret: &jtov1.SecretSource{
							Name: "db-creds",
							Key:  "password",
						},
					},
				},
				Template: "DB_PASSWORD={{ password }}",
				Output: jtov1.Output{
					Kind: "Secret",
					Name: "generated-secret",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for output Secret
		outputKey := types.NamespacedName{Name: "generated-secret", Namespace: ns.Name}
		outputSecret, err := waitForOutputSecret(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output Secret: %v", err)
		}

		expected := "DB_PASSWORD=s3cret123"
		if string(outputSecret.Data["content"]) != expected {
			t.Errorf("unexpected output content:\ngot:  %q\nwant: %q", string(outputSecret.Data["content"]), expected)
		}

		// Verify labels on Secret
		if outputSecret.Labels[controller.LabelManagedBy] != controller.ManagerName {
			t.Errorf("expected managed-by label %q, got %q", controller.ManagerName, outputSecret.Labels[controller.LabelManagedBy])
		}
	})

	t.Run("OutputNameDefaultsToCRName", func(t *testing.T) {
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name-default-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"value": "hello",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-output",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "val",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "name-default-source",
							Key:  "value",
						},
					},
				},
				Template: "VALUE={{ val }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					// Name omitted — should default to CR name "my-output"
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// The output should use the CR name
		outputKey := types.NamespacedName{Name: "my-output", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap with default name: %v", err)
		}

		if outputCM.Data["content"] != "VALUE=hello" {
			t.Errorf("unexpected output: %q", outputCM.Data["content"])
		}
	})

	t.Run("CustomOutputName", func(t *testing.T) {
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-name-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"key": "world",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cr-with-custom-output",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "val",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "custom-name-source",
							Key:  "key",
						},
					},
				},
				Template: "GREETING={{ val }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "custom-output-name",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "custom-output-name", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if outputCM.Data["content"] != "GREETING=world" {
			t.Errorf("unexpected output: %q", outputCM.Data["content"])
		}
	})

	t.Run("MultipleSources", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-source-cm",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"host": "redis.local",
			},
		}
		if err := tc.client.Create(ctx, cm); err != nil {
			t.Fatalf("failed to create ConfigMap: %v", err)
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-source-secret",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"token": []byte("abc123"),
			},
		}
		if err := tc.client.Create(ctx, secret); err != nil {
			t.Fatalf("failed to create Secret: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multi-source",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "redis_host",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "multi-source-cm",
							Key:  "host",
						},
					},
					{
						Name: "api_token",
						Secret: &jtov1.SecretSource{
							Name: "multi-source-secret",
							Key:  "token",
						},
					},
				},
				Template: "REDIS_HOST={{ redis_host }}\nAPI_TOKEN={{ api_token }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "multi-source", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		expected := "REDIS_HOST=redis.local\nAPI_TOKEN=abc123"
		if outputCM.Data["content"] != expected {
			t.Errorf("unexpected output:\ngot:  %q\nwant: %q", outputCM.Data["content"], expected)
		}
	})

	t.Run("NoSourcesStaticTemplate", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "static-template",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Template: "STATIC_VALUE=42\nANOTHER=hello",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "static-template", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		expected := "STATIC_VALUE=42\nANOTHER=hello"
		if outputCM.Data["content"] != expected {
			t.Errorf("unexpected output:\ngot:  %q\nwant: %q", outputCM.Data["content"], expected)
		}
	})

	t.Run("ConcurrentJinjaTemplates", func(t *testing.T) {
		numTemplates := 5
		done := make(chan bool, numTemplates)

		for i := 0; i < numTemplates; i++ {
			go func(idx int) {
				name := "concurrent-" + strings.Repeat(string(rune('a'+idx)), 1)
				localJT := &jtov1.JinjaTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: ns.Name,
					},
					Spec: jtov1.JinjaTemplateSpec{
						Template: "INDEX=" + strings.Repeat(string(rune('a'+idx)), 1),
						Output: jtov1.Output{
							Kind: "ConfigMap",
						},
					},
				}
				tc.client.Create(ctx, localJT)
				done <- true
			}(i)
		}

		for i := 0; i < numTemplates; i++ {
			<-done
		}

		// Wait for all to be processed
		time.Sleep(5 * time.Second)

		var cmList corev1.ConfigMapList
		if err := tc.client.List(ctx, &cmList, client.InNamespace(ns.Name)); err != nil {
			t.Fatalf("failed to list ConfigMaps: %v", err)
		}

		processedCount := 0
		for _, cm := range cmList.Items {
			if strings.HasPrefix(cm.Name, "concurrent-") {
				if cm.Labels[controller.LabelManagedBy] == controller.ManagerName {
					processedCount++
				}
			}
		}

		if processedCount < numTemplates {
			t.Errorf("expected at least %d concurrent templates processed, got %d", numTemplates, processedCount)
		}
	})
}

// TestExternalTemplate tests using templateFrom to load templates from a ConfigMap.
func TestExternalTemplate(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("TemplateFromConfigMap", func(t *testing.T) {
		// Create ConfigMap with template
		templateCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "template-store",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"app.conf.tpl": "SERVER_HOST={{ host }}\nSERVER_PORT={{ port }}",
			},
		}
		if err := tc.client.Create(ctx, templateCM); err != nil {
			t.Fatalf("failed to create template ConfigMap: %v", err)
		}

		// Create source ConfigMap
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server-config",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"host": "0.0.0.0",
				"port": "8080",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "external-tpl",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "host",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "server-config",
							Key:  "host",
						},
					},
					{
						Name: "port",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "server-config",
							Key:  "port",
						},
					},
				},
				TemplateFrom: &jtov1.TemplateFrom{
					ConfigMapRef: &jtov1.ConfigMapKeyRef{
						Name: "template-store",
						Key:  "app.conf.tpl",
					},
				},
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "external-tpl", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		expected := "SERVER_HOST=0.0.0.0\nSERVER_PORT=8080"
		if outputCM.Data["content"] != expected {
			t.Errorf("unexpected output:\ngot:  %q\nwant: %q", outputCM.Data["content"], expected)
		}
	})

	t.Run("MissingTemplateConfigMap", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-tpl-cm",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				TemplateFrom: &jtov1.TemplateFrom{
					ConfigMapRef: &jtov1.ConfigMapKeyRef{
						Name: "nonexistent-cm",
						Key:  "template",
					},
				},
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Should get Ready=False condition
		jtKey := types.NamespacedName{Name: "missing-tpl-cm", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				if cond.Reason != controller.ReasonTemplateLoadFailed {
					t.Errorf("expected reason %q, got %q", controller.ReasonTemplateLoadFailed, cond.Reason)
				}
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition")
		}

		// Verify no output was created
		outputKey := types.NamespacedName{Name: "missing-tpl-cm", Namespace: ns.Name}
		var outputCM corev1.ConfigMap
		err = tc.client.Get(ctx, outputKey, &outputCM)
		if err == nil && outputCM.Labels[controller.LabelManagedBy] == controller.ManagerName {
			t.Error("output ConfigMap should not have been created when template is missing")
		}
	})

	t.Run("MissingTemplateKey", func(t *testing.T) {
		templateCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "tpl-missing-key",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"other-key": "some content",
			},
		}
		if err := tc.client.Create(ctx, templateCM); err != nil {
			t.Fatalf("failed to create ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-tpl-key",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				TemplateFrom: &jtov1.TemplateFrom{
					ConfigMapRef: &jtov1.ConfigMapKeyRef{
						Name: "tpl-missing-key",
						Key:  "nonexistent-key",
					},
				},
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "missing-tpl-key", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for missing template key")
		}
	})
}

// TestLabelSelectorSources tests label selector-based source resolution.
func TestLabelSelectorSources(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("ConfigMapLabelSelector", func(t *testing.T) {
		// Create ConfigMaps with matching labels
		for i, data := range []map[string]string{
			{"url": "https://api1.example.com"},
			{"url": "https://api2.example.com"},
		} {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "endpoint-" + string(rune('a'+i)),
					Namespace: ns.Name,
					Labels: map[string]string{
						"type": "endpoint",
					},
				},
				Data: data,
			}
			if err := tc.client.Create(ctx, cm); err != nil {
				t.Fatalf("failed to create ConfigMap: %v", err)
			}
		}

		// Create a non-matching ConfigMap
		nonMatchingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-cm",
				Namespace: ns.Name,
				Labels: map[string]string{
					"type": "database",
				},
			},
			Data: map[string]string{
				"url": "https://db.example.com",
			},
		}
		if err := tc.client.Create(ctx, nonMatchingCM); err != nil {
			t.Fatalf("failed to create non-matching ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "label-selector-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "endpoints",
						ConfigMap: &jtov1.ConfigMapSource{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"type": "endpoint",
								},
							},
						},
					},
				},
				Template: "{% for ep in endpoints %}{{ ep.name }}: {{ ep.data.url }}\n{% endfor %}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "label-selector-test", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		content := outputCM.Data["content"]

		// Verify both endpoints are present
		if !strings.Contains(content, "endpoint-a: https://api1.example.com") {
			t.Errorf("output should contain endpoint-a, got: %q", content)
		}
		if !strings.Contains(content, "endpoint-b: https://api2.example.com") {
			t.Errorf("output should contain endpoint-b, got: %q", content)
		}

		// Verify non-matching ConfigMap is NOT present
		if strings.Contains(content, "other-cm") || strings.Contains(content, "db.example.com") {
			t.Errorf("output should NOT contain non-matching ConfigMap data, got: %q", content)
		}
	})

	t.Run("SecretLabelSelector", func(t *testing.T) {
		// Create Secrets with matching labels
		for i, data := range []map[string][]byte{
			{"token": []byte("token-1")},
			{"token": []byte("token-2")},
		} {
			s := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-cred-" + string(rune('a'+i)),
					Namespace: ns.Name,
					Labels: map[string]string{
						"type": "service-credential",
					},
				},
				Data: data,
			}
			if err := tc.client.Create(ctx, s); err != nil {
				t.Fatalf("failed to create Secret: %v", err)
			}
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret-label-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "credentials",
						Secret: &jtov1.SecretSource{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"type": "service-credential",
								},
							},
						},
					},
				},
				Template: "{% for cred in credentials %}{{ cred.name }}={{ cred.data.token }}\n{% endfor %}",
				Output: jtov1.Output{
					Kind: "Secret",
					Name: "aggregated-creds",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "aggregated-creds", Namespace: ns.Name}
		outputSecret, err := waitForOutputSecret(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output Secret: %v", err)
		}

		content := string(outputSecret.Data["content"])
		if !strings.Contains(content, "service-cred-a=token-1") {
			t.Errorf("output should contain service-cred-a, got: %q", content)
		}
		if !strings.Contains(content, "service-cred-b=token-2") {
			t.Errorf("output should contain service-cred-b, got: %q", content)
		}
	})

	t.Run("EmptyLabelSelectorResult", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "empty-selector",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "items",
						ConfigMap: &jtov1.ConfigMapSource{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"type": "nonexistent-label",
								},
							},
						},
					},
				},
				Template: "COUNT={{ items|length }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "empty-selector", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if outputCM.Data["content"] != "COUNT=0" {
			t.Errorf("expected empty list result, got: %q", outputCM.Data["content"])
		}
	})
}

// TestErrorHandling tests various error scenarios and their status reporting.
func TestErrorHandling(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("MissingSourceConfigMap", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-source",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "data",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "nonexistent-configmap",
							Key:  "key",
						},
					},
				},
				Template: "DATA={{ data }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "missing-source", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				if cond.Reason != controller.ReasonSourceResolutionFailed {
					t.Errorf("expected reason %q, got %q", controller.ReasonSourceResolutionFailed, cond.Reason)
				}
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for missing source")
		}
	})

	t.Run("MissingSourceSecret", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-secret-source",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "pass",
						Secret: &jtov1.SecretSource{
							Name: "nonexistent-secret",
							Key:  "password",
						},
					},
				},
				Template: "PASS={{ pass }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "missing-secret-source", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for missing secret source")
		}
	})

	t.Run("MissingKeyInSource", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "has-other-key",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"existing-key": "value",
			},
		}
		if err := tc.client.Create(ctx, cm); err != nil {
			t.Fatalf("failed to create ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "missing-key",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "val",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "has-other-key",
							Key:  "wrong-key",
						},
					},
				},
				Template: "VAL={{ val }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "missing-key", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for missing key in source")
		}
	})

	t.Run("InvalidTemplateNoTemplate", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "no-template",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "no-template", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				if cond.Reason != controller.ReasonInvalidSpec {
					t.Errorf("expected reason %q, got %q", controller.ReasonInvalidSpec, cond.Reason)
				}
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for missing template")
		}
	})

	t.Run("TemplateSyntaxError", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bad-syntax",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Template: "{% for x in items %}{{ x }}", // Missing endfor
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		jtKey := types.NamespacedName{Name: "bad-syntax", Namespace: ns.Name}
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to get JinjaTemplate: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionFalse {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=False condition for template syntax error")
		}
	})
}

// TestSourceChangeTriggersRerender tests that changes to source resources trigger re-reconciliation.
func TestSourceChangeTriggersRerender(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("ConfigMapSourceUpdate", func(t *testing.T) {
		// Create source ConfigMap
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "watched-cm",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"value": "original",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source ConfigMap: %v", err)
		}

		// Create JinjaTemplate
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "watch-cm-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "val",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "watched-cm",
							Key:  "value",
						},
					},
				},
				Template: "RESULT={{ val }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "watched-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for initial output
		outputKey := types.NamespacedName{Name: "watched-output", Namespace: ns.Name}
		outputCM, err := waitForConfigMapContent(ctx, tc.client, outputKey, "RESULT=original")
		if err != nil {
			t.Fatalf("failed to get initial output: %v", err)
		}
		if outputCM.Data["content"] != "RESULT=original" {
			t.Fatalf("unexpected initial content: %q", outputCM.Data["content"])
		}

		// Update source ConfigMap
		if err := tc.client.Get(ctx, types.NamespacedName{Name: "watched-cm", Namespace: ns.Name}, sourceCM); err != nil {
			t.Fatalf("failed to re-fetch source ConfigMap: %v", err)
		}
		sourceCM.Data["value"] = "updated"
		if err := tc.client.Update(ctx, sourceCM); err != nil {
			t.Fatalf("failed to update source ConfigMap: %v", err)
		}

		// Wait for updated output
		outputCM, err = waitForConfigMapContent(ctx, tc.client, outputKey, "RESULT=updated")
		if err != nil {
			t.Fatalf("failed to get updated output: %v", err)
		}
		if outputCM.Data["content"] != "RESULT=updated" {
			t.Errorf("expected updated content, got: %q", outputCM.Data["content"])
		}
	})

	t.Run("SecretSourceUpdate", func(t *testing.T) {
		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "watched-secret",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"password": []byte("old-password"),
			},
		}
		if err := tc.client.Create(ctx, sourceSecret); err != nil {
			t.Fatalf("failed to create source Secret: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "watch-secret-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "pass",
						Secret: &jtov1.SecretSource{
							Name: "watched-secret",
							Key:  "password",
						},
					},
				},
				Template: "PASSWORD={{ pass }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "secret-watch-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for initial output
		outputKey := types.NamespacedName{Name: "secret-watch-output", Namespace: ns.Name}
		outputCM, err := waitForConfigMapContent(ctx, tc.client, outputKey, "PASSWORD=old-password")
		if err != nil {
			t.Fatalf("failed to get initial output: %v", err)
		}
		if outputCM.Data["content"] != "PASSWORD=old-password" {
			t.Fatalf("unexpected initial content: %q", outputCM.Data["content"])
		}

		// Update source Secret
		if err := tc.client.Get(ctx, types.NamespacedName{Name: "watched-secret", Namespace: ns.Name}, sourceSecret); err != nil {
			t.Fatalf("failed to re-fetch source Secret: %v", err)
		}
		sourceSecret.Data["password"] = []byte("new-password")
		if err := tc.client.Update(ctx, sourceSecret); err != nil {
			t.Fatalf("failed to update source Secret: %v", err)
		}

		// Wait for updated output
		outputCM, err = waitForConfigMapContent(ctx, tc.client, outputKey, "PASSWORD=new-password")
		if err != nil {
			t.Fatalf("failed to get updated output: %v", err)
		}
		if outputCM.Data["content"] != "PASSWORD=new-password" {
			t.Errorf("expected updated content, got: %q", outputCM.Data["content"])
		}
	})

	t.Run("SourceRecoveryAfterCreation", func(t *testing.T) {
		// Create JinjaTemplate BEFORE the source exists
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "recovery-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "val",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "late-source",
							Key:  "data",
						},
					},
				},
				Template: "DATA={{ val }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "recovery-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Verify it fails initially
		jtKey := types.NamespacedName{Name: "recovery-test", Namespace: ns.Name}
		_, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionFalse)
		if err != nil {
			t.Fatalf("failed to verify initial failure: %v", err)
		}

		// Now create the source ConfigMap
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "late-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"data": "recovered-value",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create late source ConfigMap: %v", err)
		}

		// Now it should succeed
		outputKey := types.NamespacedName{Name: "recovery-output", Namespace: ns.Name}
		outputCM, err := waitForConfigMapContent(ctx, tc.client, outputKey, "DATA=recovered-value")
		if err != nil {
			t.Fatalf("failed to get recovered output: %v", err)
		}
		if outputCM.Data["content"] != "DATA=recovered-value" {
			t.Errorf("expected recovered content, got: %q", outputCM.Data["content"])
		}

		// Verify condition is now Ready=True
		updatedJT, err := waitForCondition(ctx, tc.client, jtKey, controller.ConditionReady, metav1.ConditionTrue)
		if err != nil {
			t.Fatalf("failed to verify recovery: %v", err)
		}

		found := false
		for _, cond := range updatedJT.Status.Conditions {
			if cond.Type == controller.ConditionReady && cond.Status == metav1.ConditionTrue {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected Ready=True after source recovery")
		}
	})
}

// TestOwnerReference tests owner reference behavior.
func TestOwnerReference(t *testing.T) {
	t.Run("OwnerReferenceSetByDefault", func(t *testing.T) {
		tc := setupTestManager(t, nil) // Default: owner reference enabled
		ns := createNamespace(t, tc.client)
		defer tc.cleanup(t, ns)

		ctx := context.Background()

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-ref-default",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Template: "STATIC=value",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "owner-ref-default", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if len(outputCM.OwnerReferences) == 0 {
			t.Error("expected owner reference to be set by default")
		} else {
			ownerRef := outputCM.OwnerReferences[0]
			if ownerRef.Name != "owner-ref-default" {
				t.Errorf("expected owner reference name %q, got %q", "owner-ref-default", ownerRef.Name)
			}
			if ownerRef.Kind != "JinjaTemplate" {
				t.Errorf("expected owner reference kind %q, got %q", "JinjaTemplate", ownerRef.Kind)
			}
		}
	})

	t.Run("OwnerReferenceExplicitlyEnabled", func(t *testing.T) {
		tc := setupTestManager(t, nil)
		ns := createNamespace(t, tc.client)
		defer tc.cleanup(t, ns)

		ctx := context.Background()

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-ref-enabled",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				SetOwnerReference: boolPtr(true),
				Template:          "STATIC=enabled",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "owner-ref-enabled", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if len(outputCM.OwnerReferences) == 0 {
			t.Error("expected owner reference to be set when explicitly enabled")
		}
	})

	t.Run("OwnerReferenceExplicitlyDisabled", func(t *testing.T) {
		tc := setupTestManager(t, nil)
		ns := createNamespace(t, tc.client)
		defer tc.cleanup(t, ns)

		ctx := context.Background()

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-ref-disabled",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				SetOwnerReference: boolPtr(false),
				Template:          "STATIC=disabled",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "owner-ref-disabled", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if len(outputCM.OwnerReferences) != 0 {
			t.Errorf("expected NO owner reference when explicitly disabled, got %d", len(outputCM.OwnerReferences))
		}
	})

	t.Run("GlobalDefaultOwnerReferenceDisabled", func(t *testing.T) {
		customConfig := &config.OperatorConfig{
			DefaultOwnerReference: false,
		}
		tc := setupTestManager(t, customConfig)
		ns := createNamespace(t, tc.client)
		defer tc.cleanup(t, ns)

		ctx := context.Background()

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "global-no-owner",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				// SetOwnerReference omitted — should use global default (false)
				Template: "STATIC=no-owner",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "global-no-owner", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if len(outputCM.OwnerReferences) != 0 {
			t.Errorf("expected NO owner reference when global default is false, got %d", len(outputCM.OwnerReferences))
		}
	})

	t.Run("CROverridesGlobalDefault", func(t *testing.T) {
		customConfig := &config.OperatorConfig{
			DefaultOwnerReference: false, // Global default: no owner ref
		}
		tc := setupTestManager(t, customConfig)
		ns := createNamespace(t, tc.client)
		defer tc.cleanup(t, ns)

		ctx := context.Background()

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cr-overrides-global",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				SetOwnerReference: boolPtr(true), // CR override: yes owner ref
				Template:          "STATIC=override",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "cr-overrides-global", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if len(outputCM.OwnerReferences) == 0 {
			t.Error("expected owner reference when CR overrides global default")
		}
	})
}

// TestJinjaTemplateFeatures tests advanced Jinja template features.
func TestJinjaTemplateFeatures(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("ForLoop", func(t *testing.T) {
		// Create multiple ConfigMaps with labels
		for i := 0; i < 3; i++ {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "loop-item-" + string(rune('a'+i)),
					Namespace: ns.Name,
					Labels: map[string]string{
						"app": "loop-test",
					},
				},
				Data: map[string]string{
					"value": "item-" + string(rune('a'+i)),
				},
			}
			if err := tc.client.Create(ctx, cm); err != nil {
				t.Fatalf("failed to create ConfigMap: %v", err)
			}
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "loop-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "items",
						ConfigMap: &jtov1.ConfigMapSource{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": "loop-test",
								},
							},
						},
					},
				},
				Template: "{% for item in items %}{{ item.data.value }}\n{% endfor %}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "loop-test", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		content := outputCM.Data["content"]
		for i := 0; i < 3; i++ {
			expected := "item-" + string(rune('a'+i))
			if !strings.Contains(content, expected) {
				t.Errorf("output should contain %q, got: %q", expected, content)
			}
		}
	})

	t.Run("ConditionalRendering", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cond-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"env": "production",
			},
		}
		if err := tc.client.Create(ctx, cm); err != nil {
			t.Fatalf("failed to create ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "conditional-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "env",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "cond-source",
							Key:  "env",
						},
					},
				},
				Template: "{% if env == 'production' %}LOG_LEVEL=warn{% else %}LOG_LEVEL=debug{% endif %}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "conditional-test", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		if outputCM.Data["content"] != "LOG_LEVEL=warn" {
			t.Errorf("unexpected conditional output: %q", outputCM.Data["content"])
		}
	})

	t.Run("FilterUsage", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "filter-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"name": "hello world",
			},
		}
		if err := tc.client.Create(ctx, cm); err != nil {
			t.Fatalf("failed to create ConfigMap: %v", err)
		}

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "filter-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "name",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "filter-source",
							Key:  "name",
						},
					},
				},
				Template: "UPPER={{ name|upper }}\nLOWER={{ name|lower }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "filter-test", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		content := outputCM.Data["content"]
		if !strings.Contains(content, "UPPER=HELLO WORLD") {
			t.Errorf("expected upper filter result, got: %q", content)
		}
		if !strings.Contains(content, "LOWER=hello world") {
			t.Errorf("expected lower filter result, got: %q", content)
		}
	})
}

// TestCRDeletion tests behavior when the JinjaTemplate CR is deleted.
func TestCRDeletion(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("DeletionWithOwnerReference", func(t *testing.T) {
		// NOTE: envtest does NOT run the Kubernetes garbage collector, so we cannot
		// test actual cascading deletion via OwnerReferences here. Instead, we verify
		// that the OwnerReference is correctly set on the output resource, which is
		// the controller's responsibility. Actual garbage collection behavior should
		// be validated in E2E tests against a real cluster (e.g. Kind).

		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "delete-with-owner",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				SetOwnerReference: boolPtr(true),
				Template:          "DELETEME=true",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for output
		outputKey := types.NamespacedName{Name: "delete-with-owner", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		// Verify OwnerReference is set correctly on the output
		if len(outputCM.OwnerReferences) == 0 {
			t.Fatal("expected output ConfigMap to have an OwnerReference")
		}

		ownerRef := outputCM.OwnerReferences[0]
		if ownerRef.Name != jt.Name {
			t.Errorf("expected OwnerReference name %q, got %q", jt.Name, ownerRef.Name)
		}
		if ownerRef.Kind != "JinjaTemplate" {
			t.Errorf("expected OwnerReference kind %q, got %q", "JinjaTemplate", ownerRef.Kind)
		}
		if ownerRef.APIVersion != "jto.gtrfc.com/v1" {
			t.Errorf("expected OwnerReference apiVersion %q, got %q", "jto.gtrfc.com/v1", ownerRef.APIVersion)
		}

		// Verify the CR can be deleted without error
		if err := tc.client.Delete(ctx, jt); err != nil {
			t.Fatalf("failed to delete JinjaTemplate: %v", err)
		}

		// Verify the CR is actually gone
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			var check jtov1.JinjaTemplate
			err := tc.client.Get(ctx, types.NamespacedName{Name: jt.Name, Namespace: ns.Name}, &check)
			if apierrors.IsNotFound(err) {
				return // CR deleted successfully
			}
			time.Sleep(interval)
		}
		t.Error("JinjaTemplate CR was not deleted within timeout")
	})

	t.Run("DeletionWithoutOwnerReference", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "delete-no-owner",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				SetOwnerReference: boolPtr(false),
				Template:          "KEEPME=true",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "persistent-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "persistent-output", Namespace: ns.Name}
		_, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output ConfigMap: %v", err)
		}

		// Delete the CR
		if err := tc.client.Delete(ctx, jt); err != nil {
			t.Fatalf("failed to delete JinjaTemplate: %v", err)
		}

		// Wait a bit and verify output survives
		time.Sleep(3 * time.Second)

		var cm corev1.ConfigMap
		err = tc.client.Get(ctx, outputKey, &cm)
		if apierrors.IsNotFound(err) {
			t.Error("output ConfigMap should survive CR deletion when no owner reference is set")
		} else if err != nil {
			t.Fatalf("unexpected error checking output: %v", err)
		}

		if cm.Data["content"] != "KEEPME=true" {
			t.Errorf("output content should be preserved, got: %q", cm.Data["content"])
		}
	})
}

// TestMultipleNamespaces verifies the operator works across multiple namespaces.
func TestMultipleNamespaces(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns1 := createNamespace(t, tc.client)
	ns2 := createNamespace(t, tc.client)
	defer func() {
		tc.cleanup(t, ns1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tc.client.Delete(ctx, ns2)
	}()

	ctx := context.Background()

	// Create JinjaTemplates in different namespaces
	for _, ns := range []*corev1.Namespace{ns1, ns2} {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cross-ns-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Template: "NAMESPACE={{ namespace }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
				},
			},
		}
		// We use namespace as a static text since sources are not relevant here
		jt.Spec.Template = "NAMESPACE=" + ns.Name
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate in %s: %v", ns.Name, err)
		}
	}

	// Verify outputs in both namespaces
	for _, ns := range []*corev1.Namespace{ns1, ns2} {
		outputKey := types.NamespacedName{Name: "cross-ns-test", Namespace: ns.Name}
		outputCM, err := waitForOutputConfigMap(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output in namespace %s: %v", ns.Name, err)
		}

		expected := "NAMESPACE=" + ns.Name
		if outputCM.Data["content"] != expected {
			t.Errorf("namespace %s: expected %q, got %q", ns.Name, expected, outputCM.Data["content"])
		}
	}
}

// TestOutputUpdate tests that existing output resources are correctly updated.
func TestOutputUpdate(t *testing.T) {
	tc := setupTestManager(t, nil)
	ns := createNamespace(t, tc.client)
	defer tc.cleanup(t, ns)

	ctx := context.Background()

	t.Run("UpdateExistingOutput", func(t *testing.T) {
		// Create source
		sourceCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-source",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"val": "version1",
			},
		}
		if err := tc.client.Create(ctx, sourceCM); err != nil {
			t.Fatalf("failed to create source: %v", err)
		}

		// Create JinjaTemplate
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Sources: []jtov1.Source{
					{
						Name: "v",
						ConfigMap: &jtov1.ConfigMapSource{
							Name: "update-source",
							Key:  "val",
						},
					},
				},
				Template: "VERSION={{ v }}",
				Output: jtov1.Output{
					Kind: "ConfigMap",
					Name: "update-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		// Wait for first version
		outputKey := types.NamespacedName{Name: "update-output", Namespace: ns.Name}
		_, err := waitForConfigMapContent(ctx, tc.client, outputKey, "VERSION=version1")
		if err != nil {
			t.Fatalf("failed to get initial output: %v", err)
		}

		// Update the CR's template
		jtKey := types.NamespacedName{Name: "update-test", Namespace: ns.Name}
		if err := tc.client.Get(ctx, jtKey, jt); err != nil {
			t.Fatalf("failed to re-fetch JinjaTemplate: %v", err)
		}
		jt.Spec.Template = "APP_VERSION={{ v }}"
		if err := tc.client.Update(ctx, jt); err != nil {
			t.Fatalf("failed to update JinjaTemplate: %v", err)
		}

		// Wait for updated output
		outputCM, err := waitForConfigMapContent(ctx, tc.client, outputKey, "APP_VERSION=version1")
		if err != nil {
			t.Fatalf("failed to get updated output: %v", err)
		}
		if outputCM.Data["content"] != "APP_VERSION=version1" {
			t.Errorf("unexpected updated content: %q", outputCM.Data["content"])
		}
	})

	t.Run("SecretOutputUpdate", func(t *testing.T) {
		jt := &jtov1.JinjaTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret-update-test",
				Namespace: ns.Name,
			},
			Spec: jtov1.JinjaTemplateSpec{
				Template: "SECRET_V1=true",
				Output: jtov1.Output{
					Kind: "Secret",
					Name: "secret-update-output",
				},
			},
		}
		if err := tc.client.Create(ctx, jt); err != nil {
			t.Fatalf("failed to create JinjaTemplate: %v", err)
		}

		outputKey := types.NamespacedName{Name: "secret-update-output", Namespace: ns.Name}
		outputSecret, err := waitForOutputSecret(ctx, tc.client, outputKey)
		if err != nil {
			t.Fatalf("failed to get output Secret: %v", err)
		}
		if string(outputSecret.Data["content"]) != "SECRET_V1=true" {
			t.Fatalf("unexpected initial Secret content: %q", string(outputSecret.Data["content"]))
		}

		// Update CR
		jtKey := types.NamespacedName{Name: "secret-update-test", Namespace: ns.Name}
		if err := tc.client.Get(ctx, jtKey, jt); err != nil {
			t.Fatalf("failed to re-fetch JinjaTemplate: %v", err)
		}
		jt.Spec.Template = "SECRET_V2=true"
		if err := tc.client.Update(ctx, jt); err != nil {
			t.Fatalf("failed to update JinjaTemplate: %v", err)
		}

		// Wait for updated Secret
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if err := tc.client.Get(ctx, outputKey, outputSecret); err != nil {
				time.Sleep(interval)
				continue
			}
			if string(outputSecret.Data["content"]) == "SECRET_V2=true" {
				break
			}
			time.Sleep(interval)
		}

		if string(outputSecret.Data["content"]) != "SECRET_V2=true" {
			t.Errorf("expected Secret content to be updated, got: %q", string(outputSecret.Data["content"]))
		}
	})
}
