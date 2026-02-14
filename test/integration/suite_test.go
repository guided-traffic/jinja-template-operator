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
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/controller"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
	tmpl "github.com/guided-traffic/jinja-template-operator/internal/template"
)

var (
	restConfig *rest.Config
	testEnv    *envtest.Environment

	// Counter for unique controller names to avoid conflicts between tests
	controllerCounter int64
)

func TestMain(m *testing.M) {
	// Configure logger without stacktraces for cleaner test output
	logf.SetLogger(zap.New(
		zap.WriteTo(os.Stdout),
		zap.UseDevMode(false),
		zap.StacktraceLevel(zapcore.PanicLevel),
	))

	// KUBEBUILDER_ASSETS must be set by the Makefile via setup-envtest
	// Run tests with: make test-integration
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		logf.Log.Error(nil, "KUBEBUILDER_ASSETS environment variable is not set. Run tests using 'make test-integration' or 'make test-integration-coverage'")
		os.Exit(1)
	}

	// Configure envtest with CRD paths
	projectRoot := getProjectRoot()
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(projectRoot, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	restConfig, err = testEnv.Start()
	if err != nil {
		logf.Log.Error(err, "failed to start test environment")
		os.Exit(1)
	}

	// Register schemes
	err = corev1.AddToScheme(scheme.Scheme)
	if err != nil {
		logf.Log.Error(err, "failed to add corev1 to scheme")
		os.Exit(1)
	}

	err = jtov1.AddToScheme(scheme.Scheme)
	if err != nil {
		logf.Log.Error(err, "failed to add jtov1 to scheme")
		os.Exit(1)
	}

	// Run tests
	code := m.Run()

	// Cleanup
	func() {
		defer func() {
			if r := recover(); r != nil {
				logf.Log.Info("recovered from panic during cleanup", "panic", r)
			}
		}()
		if err := testEnv.Stop(); err != nil {
			logf.Log.Error(err, "failed to stop test environment (ignoring)")
		}
	}()

	os.Exit(code)
}

// testContext holds test dependencies.
type testContext struct {
	client client.Client
	cancel context.CancelFunc
}

// setupTestManager creates a manager with a unique controller name for test isolation.
func setupTestManager(t *testing.T, operatorConfig *config.OperatorConfig) *testContext {
	t.Helper()

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics to avoid port conflicts
		},
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if operatorConfig == nil {
		operatorConfig = config.NewOperatorConfig()
	}

	mgrClient := mgr.GetClient()

	reconciler := &controller.JinjaTemplateReconciler{
		Client:   mgrClient,
		Scheme:   mgr.GetScheme(),
		Config:   operatorConfig,
		Recorder: mgr.GetEventRecorder("jinjatemplate-controller"),
		Renderer: tmpl.NewRenderer(),
		Resolver: sources.NewResolver(mgrClient),
	}

	// Use unique controller name using atomic counter
	counter := atomic.AddInt64(&controllerCounter, 1)
	controllerName := "jt-controller-" + time.Now().Format("150405") + "-" + string(rune('a'+counter%26))

	err = reconciler.SetupWithManagerAndName(mgr, controllerName)
	if err != nil {
		t.Fatalf("failed to setup controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Logf("manager stopped: %v", err)
		}
	}()

	// Wait for manager and cache to be ready
	time.Sleep(500 * time.Millisecond)

	return &testContext{
		client: mgrClient,
		cancel: cancel,
	}
}

// cleanup stops the manager and removes namespace.
func (tc *testContext) cleanup(t *testing.T, ns *corev1.Namespace) {
	t.Helper()

	// Cancel context to stop manager
	tc.cancel()

	// Delete namespace
	if ns != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tc.client.Delete(ctx, ns)
	}
}

// createNamespace creates a unique namespace for test isolation.
func createNamespace(t *testing.T, c client.Client) *corev1.Namespace {
	t.Helper()

	ns := &corev1.Namespace{
		ObjectMeta: ctrl.ObjectMeta{
			GenerateName: "test-jt-",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}

	return ns
}

// getProjectRoot returns the project root directory.
func getProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	// test/integration -> project root
	return filepath.Join(dir, "..", "..")
}

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool {
	return &b
}
