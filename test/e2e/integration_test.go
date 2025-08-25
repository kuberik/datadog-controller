/*
Copyright 2025.

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
	"fmt"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kuberik/datadog-controller/test/utils"
)

var _ = Describe("Integration Tests", func() {
	Context("End-to-end workflow", func() {
		const monitorName = "integration-test-monitor"
		const resourceNamespace = "default"

		BeforeEach(func() {
			// Clean up any existing resources
			cmd := exec.Command("kubectl", "delete", "datadogmonitor", "--all", "-n", resourceNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "healthcheck", "--all", "-n", resourceNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		AfterEach(func() {
			// Clean up any remaining resources
			cmd := exec.Command("kubectl", "delete", "datadogmonitor", "--all", "-n", resourceNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "healthcheck", "--all", "-n", resourceNamespace, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
		})

		It("Should create HealthCheck and update status when DatadogMonitor status changes", func() {
			By("Creating a DatadogMonitor with health-check annotation")
			monitorYaml := fmt.Sprintf(`apiVersion: datadoghq.com/v1alpha1
kind: DatadogMonitor
metadata:
  name: %s
  namespace: %s
  annotations:
    kuberik.com/health-check: "true"
spec:
  name: "Integration Test Monitor"
  type: "metric alert"
  query: "avg(last_5m):avg:system.cpu.user{*} > 80"
status:
  monitorState: "OK"`, monitorName, resourceNamespace)

			// Write YAML to temporary file
			tmpFile, err := os.CreateTemp("", "monitor-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile.Name())
			_, err = tmpFile.WriteString(monitorYaml)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()

			// Apply the monitor
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile.Name())
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck to be created by DatadogMonitor controller")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace)
				_, err := utils.Run(cmd)
				return err
			}, time.Second*15, time.Millisecond*250).Should(Succeed())

			By("Waiting for the HealthCheck status to be updated to Healthy by HealthCheck controller")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.status.status}")
				output, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return output
			}, time.Second*15, time.Millisecond*250).Should(Equal("Healthy"))

			By("Verifying the HealthCheck has correct metadata and owner reference")
			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.metadata.labels.kuberik\\.com/datadog-monitor}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(monitorName))

			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.metadata.labels.kuberik\\.com/managed-by}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("datadog-controller"))

			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("DatadogMonitor"))

			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(monitorName))

			By("Updating the DatadogMonitor status to Alert")
			cmd = exec.Command("kubectl", "patch", "datadogmonitor", monitorName, "-n", resourceNamespace, "--type", "merge", "-p", `{"status":{"monitorState":"Alert"}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck status to be updated to Unhealthy")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.status.status}")
				output, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return output
			}, time.Second*15, time.Millisecond*250).Should(Equal("Unhealthy"))

			By("Verifying the HealthCheck has an error time")
			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.status.lastErrorTime}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).NotTo(BeEmpty())

			By("Updating the DatadogMonitor status back to OK")
			cmd = exec.Command("kubectl", "patch", "datadogmonitor", monitorName, "-n", resourceNamespace, "--type", "merge", "-p", `{"status":{"monitorState":"OK"}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck status to be updated back to Healthy")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.status.status}")
				output, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return output
			}, time.Second*15, time.Millisecond*250).Should(Equal("Healthy"))

			By("Verifying the HealthCheck has no error time")
			cmd = exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace, "-o", "jsonpath={.status.lastErrorTime}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(BeEmpty())
		})

		It("Should handle lifecycle: create, update, delete", func() {
			By("Creating a DatadogMonitor with health-check annotation")
			monitorYaml := fmt.Sprintf(`apiVersion: datadoghq.com/v1alpha1
kind: DatadogMonitor
metadata:
  name: %s
  namespace: %s
  annotations:
    kuberik.com/health-check: "true"
spec:
  name: "Lifecycle Test Monitor"
  type: "metric alert"
  query: "avg(last_5m):avg:system.cpu.user{*} > 80"`, monitorName, resourceNamespace)

			// Write YAML to temporary file
			tmpFile, err := os.CreateTemp("", "monitor-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile.Name())
			_, err = tmpFile.WriteString(monitorYaml)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()

			// Apply the monitor
			cmd := exec.Command("kubectl", "apply", "-f", tmpFile.Name())
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck to be created")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace)
				_, err := utils.Run(cmd)
				return err
			}, time.Second*15, time.Millisecond*250).Should(Succeed())

			By("Removing the health-check annotation")
			cmd = exec.Command("kubectl", "patch", "datadogmonitor", monitorName, "-n", resourceNamespace, "--type", "merge", "-p", `{"metadata":{"annotations":{}}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck to be deleted")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace)
				_, err := utils.Run(cmd)
				return err
			}, time.Second*15, time.Millisecond*250).Should(HaveOccurred())

			By("Adding the health-check annotation back")
			cmd = exec.Command("kubectl", "patch", "datadogmonitor", monitorName, "-n", resourceNamespace, "--type", "merge", "-p", `{"metadata":{"annotations":{"kuberik.com/health-check":"true"}}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the HealthCheck to be created again")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace)
				_, err := utils.Run(cmd)
				return err
			}, time.Second*15, time.Millisecond*250).Should(Succeed())

			By("Deleting the DatadogMonitor")
			cmd = exec.Command("kubectl", "delete", "datadogmonitor", monitorName, "-n", resourceNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the HealthCheck is also deleted (due to owner reference)")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "healthcheck", "datadog-check-"+monitorName, "-n", resourceNamespace)
				_, err := utils.Run(cmd)
				return err
			}, time.Second*15, time.Millisecond*250).Should(HaveOccurred())
		})
	})
})
