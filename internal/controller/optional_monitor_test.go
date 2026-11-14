package controller

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	datadoghqcomv1alpha1 "github.com/DataDog/datadog-operator/api/datadoghq/v1alpha1"
)

var _ = Describe("without the DatadogMonitor CRD", func() {
	It("detects the missing CRD and starts the manager without the monitor watch", func() {
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join("..", "..", "vendir", "crds", "kuberik")},
			ErrorIfCRDPathMissing: true,
		}
		if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
			env.BinaryAssetsDirectory = dir
		}
		c, err := env.Start()
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(env.Stop()).To(Succeed()) }()

		installed, err := APIResourceInstalled(c, datadoghqcomv1alpha1.GroupVersion, "DatadogMonitor")
		Expect(err).NotTo(HaveOccurred())
		Expect(installed).To(BeFalse())

		// The same check against the main suite's API server (CRD present).
		installed, err = APIResourceInstalled(cfg, datadoghqcomv1alpha1.GroupVersion, "DatadogMonitor")
		Expect(err).NotTo(HaveOccurred())
		Expect(installed).To(BeTrue())

		mgr, err := ctrl.NewManager(c, ctrl.Options{
			Scheme:     scheme.Scheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{CacheSyncTimeout: 5 * time.Second},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect((&HealthCheckReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), SkipMonitorWatch: true,
		}).SetupWithManager(mgr)).To(Succeed())

		mctx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		errCh := make(chan error, 1)
		go func() { errCh <- mgr.Start(mctx) }()
		// With the watch enabled the manager exits after the 5s cache sync
		// timeout ("timed out waiting for cache to be synced").
		Consistently(errCh, 9*time.Second).ShouldNot(Receive())
		stop()
		Eventually(errCh, 10*time.Second).Should(Receive(BeNil()))
	})
})
