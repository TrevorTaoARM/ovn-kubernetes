package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo"

	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/framework"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

const (
	// IANA assigned VXLAN UDP port - rfc7348
	vxlanPort = "4789"
)

func checkContinuousConnectivity(f *framework.Framework, nodeName, podName, host string, port, timeout int, podChan chan *v1.Pod, errChan chan error) {
	contName := fmt.Sprintf("%s-container", podName)

	command := []string{
		"bash", "-c",
		"set -xe; for i in {1..10}; do nc -vz -w " + strconv.Itoa(timeout) + " " + host + " " + strconv.Itoa(port) + "; sleep 2; done",
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    contName,
					Image:   framework.AgnHostImage,
					Command: command,
				},
			},
			NodeName:      nodeName,
			RestartPolicy: v1.RestartPolicyNever,
		},
	}
	podClient := f.ClientSet.CoreV1().Pods(f.Namespace.Name)
	_, err := podClient.Create(pod)
	if err != nil {
		errChan <- err
		return
	}

	err = e2epod.WaitForPodNotPending(f.ClientSet, f.Namespace.Name, podName)
	if err != nil {
		errChan <- err
		return
	}

	podGet, err := podClient.Get(podName, metav1.GetOptions{})
	if err != nil {
		errChan <- err
		return
	}

	podChan <- podGet

	err = e2epod.WaitForPodSuccessInNamespace(f.ClientSet, podName, f.Namespace.Name)

	if err != nil {
		logs, logErr := e2epod.GetPodLogs(f.ClientSet, f.Namespace.Name, pod.Name, contName)
		if logErr != nil {
			framework.Logf("Warning: Failed to get logs from pod %q: %v", pod.Name, logErr)
		} else {
			framework.Logf("pod %s/%s logs:\n%s", f.Namespace.Name, pod.Name, logs)
		}
	}

	errChan <- err
}

// pingCommand is the type to hold ping command.
type pingCommand string

const (
	// ipv4PingCommand is a ping command for IPv4.
	ipv4PingCommand pingCommand = "ping"
	// ipv6PingCommand is a ping command for IPv6.
	ipv6PingCommand pingCommand = "ping6"
)

// Place the workload on the specified node to test external connectivity
func checkConnectivityPingToHost(f *framework.Framework, nodeName, podName, host string, pingCmd pingCommand, timeout int) error {
	contName := fmt.Sprintf("%s-container", podName)
	// Ping options are:
	// -c sends 3 pings
	// -W wait at most 2 seconds for a reply
	// -w timeout
	command := []string{"/bin/sh", "-c"}
	args := []string{fmt.Sprintf("sleep 20; %s -c 3 -W 2 -w %s %s", string(pingCmd), strconv.Itoa(timeout), host)}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    contName,
					Image:   framework.AgnHostImage,
					Command: command,
					Args:    args,
				},
			},
			NodeName:      nodeName,
			RestartPolicy: v1.RestartPolicyNever,
		},
	}
	podClient := f.ClientSet.CoreV1().Pods(f.Namespace.Name)
	_, err := podClient.Create(pod)
	if err != nil {
		return err
	}
	err = e2epod.WaitForPodSuccessInNamespace(f.ClientSet, podName, f.Namespace.Name)

	if err != nil {
		logs, logErr := e2epod.GetPodLogs(f.ClientSet, f.Namespace.Name, pod.Name, contName)
		if logErr != nil {
			framework.Logf("Warning: Failed to get logs from pod %q: %v", pod.Name, logErr)
		} else {
			framework.Logf("pod %s/%s logs:\n%s", f.Namespace.Name, pod.Name, logs)
		}
	}

	return err
}

// Create a pod on the specified node using the agnostic host image
func createGenericPod(f *framework.Framework, podName, nodeSelector string, command []string) {
	contName := fmt.Sprintf("%s-container", podName)

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    contName,
					Image:   framework.AgnHostImage,
					Command: command,
				},
			},
			NodeName:      nodeSelector,
			RestartPolicy: v1.RestartPolicyNever,
		},
	}
	podClient := f.ClientSet.CoreV1().Pods(f.Namespace.Name)
	_, err := podClient.Create(pod)
	if err != nil {
		framework.Logf("Warning: Failed to get logs from pod %q: %v", pod.Name, err)
	}
	err = e2epod.WaitForPodNotPending(f.ClientSet, podName, f.Namespace.Name)
	if err != nil {
		logs, logErr := e2epod.GetPodLogs(f.ClientSet, f.Namespace.Name, pod.Name, contName)
		if logErr != nil {
			framework.Logf("Warning: Failed to get logs from pod %q: %v", pod.Name, logErr)
		} else {
			framework.Logf("pod %s/%s logs:\n%s", f.Namespace.Name, pod.Name, logs)
		}
	}
}

// Get the IP address of a pod in the specified namespace
func getPodAddress(podName, namespace string) (string, error) {
	podIP, err := framework.RunKubectl("get", "pods", podName, "--template={{.status.podIP}}", "-n"+namespace)
	if err != nil {
		framework.Failf("Unable to retrieve the IP for pod %s %v", podName, err)
		return "", err
	}
	return podIP, nil
}

// runCommand runs the cmd and returns the combined stdout and stderr
func runCommand(cmd ...string) (string, error) {
	output, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run %q: %s (%s)", strings.Join(cmd, " "), err, output)
	}
	return string(output), nil
}

var _ = Describe("e2e control plane", func() {
	var svcname = "nettest"

	f := framework.NewDefaultFramework(svcname)

	ginkgo.BeforeEach(func() {
		// Assert basic external connectivity.
		// Since this is not really a test of kubernetes in any way, we
		// leave it as a pre-test assertion, rather than a Ginko test.
		ginkgo.By("Executing a successful http request from the external internet")
		resp, err := http.Get("http://google.com")
		if err != nil {
			framework.Failf("Unable to connect/talk to the internet: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			framework.Failf("Unexpected error code, expected 200, got, %v (%v)", resp.StatusCode, resp)
		}
	})

	ginkgo.It("should provide Internet connection continuously when ovn-k8s pod is killed", func() {
		ginkgo.By("Running container which tries to connect to 8.8.8.8 in a loop")

		podChan, errChan := make(chan *v1.Pod), make(chan error)
		go checkContinuousConnectivity(f, "", "connectivity-test-continuous", "8.8.8.8", 53, 30, podChan, errChan)

		testPod := <-podChan
		framework.Logf("Test pod running on %q", testPod.Spec.NodeName)

		time.Sleep(5 * time.Second)

		podClient := f.ClientSet.CoreV1().Pods("ovn-kubernetes")

		podList, _ := podClient.List(metav1.ListOptions{})
		podName := ""
		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, "ovnkube-node") && pod.Spec.NodeName == testPod.Spec.NodeName {
				podName = pod.Name
				break
			}
		}

		err := podClient.Delete(podName, metav1.NewDeleteOptions(0))
		framework.ExpectNoError(err, "should delete ovnkube-node pod")
		framework.Logf("Deleted ovnkube-node %q", podName)

		framework.ExpectNoError(<-errChan)
	})

	ginkgo.It("should provide Internet connection continuously when master is killed", func() {
		ginkgo.By("Running container which tries to connect to 8.8.8.8 in a loop")

		podChan, errChan := make(chan *v1.Pod), make(chan error)
		go checkContinuousConnectivity(f, "", "connectivity-test-continuous", "8.8.8.8", 53, 30, podChan, errChan)

		testPod := <-podChan
		framework.Logf("Test pod running on %q", testPod.Spec.NodeName)

		time.Sleep(5 * time.Second)

		podClient := f.ClientSet.CoreV1().Pods("ovn-kubernetes")

		podList, _ := podClient.List(metav1.ListOptions{})
		podName := ""
		for _, pod := range podList.Items {
			if strings.HasPrefix(pod.Name, "ovnkube-master") {
				podName = pod.Name
				break
			}
		}

		err := podClient.Delete(podName, metav1.NewDeleteOptions(0))
		framework.ExpectNoError(err, "should delete ovnkube-master pod")
		framework.Logf("Deleted ovnkube-master %q", podName)

		framework.ExpectNoError(<-errChan)
	})
})

// Test e2e hybrid sdn inter-node connectivity between worker nodes and validate pods do not traverse the external gateway
var _ = Describe("test e2e inter-node connectivity between worker nodes hybrid overlay on separate worker nodes", func() {
	const (
		svcname          string = "internode-hyb-sdn-e2e"
		pingTarget       string = "172.17.0.250"
		ovnNs            string = "ovn-kubernetes"
		ovnWorkerNode    string = "ovn-worker"
		ovnWorkerNode2   string = "ovn-worker2"
		ovnHaWorkerNode2 string = "ovn-control-plane2"
		ovnHaWorkerNode3 string = "ovn-control-plane3"
		ovnContainer     string = "ovnkube-node"
		gwContainerName  string = "gw-test-container-internode"
		jsonFlag         string = "-o=jsonpath='{.items..metadata.name}'"
		getPodIPRetry    int    = 20
	)
	var (
		haMode    bool
		ovnNsFlag = fmt.Sprintf("--namespace=%s", ovnNs)
		labelFlag = fmt.Sprintf("name=%s", ovnContainer)
	)

	f := framework.NewDefaultFramework(svcname)

	// Determine what mode the CI is running in and get relevant endpoint information for the tests
	BeforeEach(func() {
		fieldSelectorFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnWorkerNode)
		fieldSelectorHaFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnHaWorkerNode2)

		// start the container that will act as an external gateway
		_, err := runCommand("docker", "run", "-itd", "--privileged", "--name", gwContainerName, "centos")
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		exVtepIP, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", gwContainerName)
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		// trim newline from the inspect output >:|
		exVtepIP = strings.TrimSuffix(exVtepIP, "\n")
		framework.Logf("The external gateway IP is %s", exVtepIP)

		annotateArgs := []string{
			"annotate",
			"namespace",
			f.Namespace.Name,
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-external-gw=%s", pingTarget),
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-vtep=%s", exVtepIP),
		}
		// Annotate the pods to route pods to hybrid-sdn bridge br-ext
		framework.Logf("Annotating the external gateway test namespace")
		framework.RunKubectlOrDie(annotateArgs...)

		// Attempt to retrieve the pod name that will run the external interface for e2e control-plane non-ha mode
		kubectlOut, err := framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorFlag)
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnWorkerNode, err)
		}
		// Attempt to retrieve the pod name that will run the external interface for e2e control-plane ha mode
		if kubectlOut == "''" {
			haMode = true
			kubectlOut, err = framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorHaFlag)
			if err != nil {
				framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnHaWorkerNode2, err)
			}
		}
		// Fail the test if no pod is matched within the specified node
		if kubectlOut == "''" {
			framework.Failf("Unable to locate container %s on any known nodes", ovnContainer)
		}
	})

	AfterEach(func() {
		// tear down the container simulating the gateway
		_, err := runCommand("docker", "rm", "-f", gwContainerName)
		if err != nil {
			framework.Failf("failed to delete the gateway test container %v", err)
		}
	})

	It("Should validate connectivity between pods with hybrid overlay on separate worker nodes and ensure br-ext is not traversed", func() {
		var err error
		var validIP net.IP
		var pingTarget string
		var ciWorkerNodeSrc string
		var ciWorkerNodeDst string
		dstPingPodName := "e2e-dst-ping-pod"
		command := []string{"bash", "-c", "sleep 20000"}

		// non-ha ci mode runs a named set of nodes with a prefix of ovn-worker
		ciWorkerNodeSrc = ovnWorkerNode
		ciWorkerNodeDst = ovnWorkerNode2
		// ha ci mode runs a named set of nodes with a prefix of ovn-control-plane
		if haMode {
			framework.Logf("Detected a HA mode KIND environment")
			ciWorkerNodeSrc = ovnHaWorkerNode2
			ciWorkerNodeDst = ovnHaWorkerNode3
		}
		By(fmt.Sprintf("Creating a container on node %s and verifying connectivity to a pod on node %s", ciWorkerNodeSrc, ciWorkerNodeDst))

		// Create the pod that will be used as the destination for the connectivity test
		createGenericPod(f, dstPingPodName, ciWorkerNodeDst, command)
		// There is a condition somewhere with e2e WaitForPodNotPending that returns ready
		// before calling for the IP address will succeed. This simply adds some retries.
		for i := 1; i < getPodIPRetry; i++ {
			pingTarget, err = getPodAddress(dstPingPodName, f.Namespace.Name)
			if err != nil {
				framework.Logf("Warning unable to query the test pod on node %s %v", ciWorkerNodeSrc, err)
			}
			validIP = net.ParseIP(pingTarget)
			if validIP != nil {
				framework.Logf("Destination ping target for %s is %s", dstPingPodName, pingTarget)
				break
			}
			time.Sleep(time.Second * 3)
			framework.Logf("Retry attempt %d to get pod IP from initializing pod %s", i, dstPingPodName)
		}
		// Fail the test if no address is ever retrieved
		if validIP == nil {
			framework.Failf("Warning: Failed to get an IP for target pod %s, test will fail", dstPingPodName)
		}
		// Spin up another pod that attempts to reach the previously started pod on separate nodes
		framework.ExpectNoError(
			checkConnectivityPingToHost(f, ciWorkerNodeSrc, "e2e-src-ping-pod", pingTarget, ipv4PingCommand, 30))

		fieldSelectorFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ciWorkerNodeSrc)
		kubectlOut, err := framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorFlag)
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ciWorkerNodeSrc, err)
		}
		ovnPodName := strings.Trim(kubectlOut, "'")
		ovnContainerFlag := fmt.Sprintf("--container=%s", ovnContainer)
		// dump the flowmods from br-ext to verify no counters are hit
		kubectlOut, err = framework.RunKubectl("exec", ovnPodName, ovnNsFlag, ovnContainerFlag, "--", "ovs-ofctl", "dump-flows", "br-ext")
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnWorkerNode, err)
		}
		for _, flowmod := range strings.Split(kubectlOut, "\n") {
			// filter out irrelevant lines from ofctl output
			if strings.Contains(flowmod, pingTarget) {
				// verify no flowmod counters were hit in br-ext for the target
				if !strings.Contains(flowmod, "n_packets=0") {
					framework.Failf("Expected packets=0 but found the flow %s", flowmod)
				}
			}
		}
	})
})

// Test e2e inter-node connectivity over br-int
var _ = Describe("test e2e inter-node connectivity between worker nodes", func() {
	const (
		svcname          string = "inter-node-e2e"
		ovnNs            string = "ovn-kubernetes"
		ovnWorkerNode    string = "ovn-worker"
		ovnWorkerNode2   string = "ovn-worker2"
		ovnHaWorkerNode2 string = "ovn-control-plane2"
		ovnHaWorkerNode3 string = "ovn-control-plane3"
		ovnContainer     string = "ovnkube-node"
		jsonFlag         string = "-o=jsonpath='{.items..metadata.name}'"
		getPodIPRetry    int    = 20
	)

	var (
		haMode    bool
		ovnNsFlag = fmt.Sprintf("--namespace=%s", ovnNs)
		labelFlag = fmt.Sprintf("name=%s", ovnContainer)
	)

	f := framework.NewDefaultFramework(svcname)

	// Determine which KIND environment is running by querying the running nodes
	BeforeEach(func() {
		fieldSelectorFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnWorkerNode)
		fieldSelectorHaFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnHaWorkerNode2)

		// Determine if the kind deployment is in HA mode or non-ha mode based on node naming
		kubectlOut, err := framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorFlag)
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnWorkerNode, err)
		}
		if kubectlOut == "''" {
			haMode = true
			kubectlOut, err = framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorHaFlag)
			if err != nil {
				framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnHaWorkerNode2, err)
			}
		}
		// Fail the test if no pod is matched within the specified node
		if kubectlOut == "''" {
			framework.Failf("Unable to locate container %s on any known nodes", ovnContainer)
		}
	})

	It("Should validate connectivity within a namespace of pods on separate nodes", func() {
		var err error
		var validIP net.IP
		var pingTarget string
		var ciWorkerNodeSrc string
		var ciWorkerNodeDst string
		dstPingPodName := "e2e-dst-ping-pod"
		command := []string{"bash", "-c", "sleep 20000"}
		// non-ha ci mode runs a named set of nodes with a prefix of ovn-worker
		ciWorkerNodeSrc = ovnWorkerNode
		ciWorkerNodeDst = ovnWorkerNode2
		// ha ci mode runs a named set of nodes with a prefix of ovn-control-plane
		if haMode {
			framework.Logf("Detected a HA mode KIND environment")
			ciWorkerNodeSrc = ovnHaWorkerNode2
			ciWorkerNodeDst = ovnHaWorkerNode3
		}
		By(fmt.Sprintf("Creating a container on node %s and verifying connectivity to a pod on node %s", ciWorkerNodeSrc, ciWorkerNodeDst))

		// Create the pod that will be used as the destination for the connectivity test
		createGenericPod(f, dstPingPodName, ciWorkerNodeDst, command)
		// There is a condition somewhere with e2e WaitForPodNotPending that returns ready
		// before calling for the IP address will succeed. This simply adds some retries.
		for i := 1; i < getPodIPRetry; i++ {
			pingTarget, err = getPodAddress(dstPingPodName, f.Namespace.Name)
			if err != nil {
				framework.Logf("Warning unable to query the test pod on node %s %v", ciWorkerNodeSrc, err)
			}
			validIP = net.ParseIP(pingTarget)
			if validIP != nil {
				framework.Logf("Destination ping target for %s is %s", dstPingPodName, pingTarget)
				break
			}
			time.Sleep(time.Second * 3)
			framework.Logf("Retry attempt %d to get pod IP from initializing pod %s", i, dstPingPodName)
		}
		// Fail the test if no address is ever retrieved
		if validIP == nil {
			framework.Failf("Warning: Failed to get an IP for target pod %s, test will fail", dstPingPodName)
		}
		// Spin up another pod that attempts to reach the previously started pod on separate nodes
		framework.ExpectNoError(
			checkConnectivityPingToHost(f, ciWorkerNodeSrc, "e2e-src-ping-pod", pingTarget, ipv4PingCommand, 30))
	})
})

// Verify pods in the namespace annotated with an external-gateway traverse the vxlan
// overlay and reach the intended external gateway vtep and gateway end to end
var _ = Describe("e2e external gateway validation", func() {
	const (
		svcname         string = "externalgw"
		ovnNs           string = "ovn-kubernetes"
		extGW           string = "10.249.0.1"
		gwContainerName string = "gw-test-container"
		ovnWorkerNode   string = "ovn-worker"
		ovnHaWorkerNode string = "ovn-control-plane2"
		ovnContainer    string = "ovnkube-node"
	)

	var (
		haMode    bool
		extGWCidr = fmt.Sprintf("%s/24", extGW)
		ovnNsFlag = fmt.Sprintf("--namespace=%s", ovnNs)
	)
	f := framework.NewDefaultFramework(svcname)

	// Determine what mode the CI is running in and get relevant endpoint information for the tests
	BeforeEach(func() {
		labelFlag := fmt.Sprintf("name=%s", ovnContainer)
		jsonFlag := "-o=jsonpath='{.items..metadata.name}'"
		fieldSelectorFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnWorkerNode)
		fieldSelectorHaFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnHaWorkerNode)
		// start the container that will act as an external gateway
		_, err := runCommand("docker", "run", "-itd", "--privileged", "--name", gwContainerName, "centos")
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		// retrieve the container ip of the external gateway container
		exVtepIP, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", gwContainerName)
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		// trim newline from the inspect output >:|
		exVtepIP = strings.TrimSuffix(exVtepIP, "\n")
		if ip := net.ParseIP(exVtepIP); ip == nil {
			framework.Failf("Unable to retrieve a valid address from container %s with inspect output of %s", gwContainerName, exVtepIP)
		}
		framework.Logf("The external gateway IP is %s", exVtepIP)
		// annotate the test namespace

		annotateArgs := []string{
			"annotate",
			"namespace",
			f.Namespace.Name,
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-external-gw=%s", extGW),
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-vtep=%s", exVtepIP),
		}

		framework.Logf("Annotating the external gateway test namespace")
		framework.RunKubectlOrDie(annotateArgs...)
		// attempt to retrieve the pod name that will source the tunnel test in non-HA mode
		kubectlOut, err := framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorFlag)
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnWorkerNode, err)
		}
		// attempt to retrieve the pod name that will source the tunnel test in HA mode
		if kubectlOut == "''" {
			haMode = true
			kubectlOut, err = framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorHaFlag)
			if err != nil {
				framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnHaWorkerNode, err)
			}
		}
	})

	AfterEach(func() {
		// tear down the container simulating the gateway
		_, err := runCommand("docker", "rm", "-f", gwContainerName)
		if err != nil {
			framework.Failf("failed to delete the gateway test container %v", err)
		}
	})

	It("Should validate connectivity to the vxlan interface simulating an external gateway and validate traffic was encapsulated", func() {
		// non-ha ci mode runs a set of kind nodes prefixed with ovn-worker
		ciWorkerNodeSrc := ovnWorkerNode
		if haMode {
			// ha ci mode runs a named set of nodes with a prefix of ovn-control-plane
			ciWorkerNodeSrc = ovnHaWorkerNode
		}
		localVtepIP, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", ciWorkerNodeSrc)
		if err != nil {
			framework.Failf("failed to get the node ip address from node %s %v", ciWorkerNodeSrc, err)
		}
		localVtepIP = strings.TrimSuffix(localVtepIP, "\n")
		if ip := net.ParseIP(localVtepIP); ip == nil {
			framework.Failf("Unable to retrieve a valid address from container %s with inspect output of %s", gwContainerName, localVtepIP)
		}
		framework.Logf("the pod side vtep node is %s and the ip %s", ciWorkerNodeSrc, localVtepIP)
		// retrieve the pod cidr for the worker node
		jsonFlag := "jsonpath='{.metadata.annotations.k8s\\.ovn\\.org/node-subnets}'"
		kubectlOut, err := framework.RunKubectl("get", "node", ciWorkerNodeSrc, "-o", jsonFlag)
		if err != nil {
			framework.Failf("Error retrieving the pod cidr from %s %v", ciWorkerNodeSrc, err)
		}
		// strip the apostrophe from stdout and parse the pod cidr
		annotation := strings.Replace(kubectlOut, "'", "", -1)
		defaultSubnet := make(map[string]string)
		if err := json.Unmarshal([]byte(annotation), &defaultSubnet); err != nil {
			framework.Failf("Error parsing the pod cidr from %s %v", ciWorkerNodeSrc, err)
		}
		podCIDR := defaultSubnet["default"]
		framework.Logf("the pod cidr for node %s is %s", ciWorkerNodeSrc, podCIDR)
		// setup the container to act as an external gateway and vtep
		_, err = runCommand("docker", "exec", gwContainerName, "ip", "link", "add", "vxlan0", "type", "vxlan", "dev",
			"eth0", "id", "4097", "dstport", vxlanPort, "remote", localVtepIP)
		if err != nil {
			framework.Failf("failed to create the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerName, "ip", "link", "set", "vxlan0", "up")
		if err != nil {
			framework.Failf("failed to enable the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerName, "ip", "address", "add", extGWCidr, "dev", "lo")
		if err != nil {
			framework.Failf("failed to add the external gateway ip to dev lo on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerName, "ip", "route", "add", podCIDR, "dev", "vxlan0")
		if err != nil {
			framework.Failf("failed to add the pod route on the test container: %v", err)
		}
		// give the container time to come up and stabilize
		time.Sleep(time.Second * 10)
		By(fmt.Sprintf("Creating a container on %s and testing end to end traffic to an external gateway", ciWorkerNodeSrc))
		framework.ExpectNoError(
			// generate traffic that will being encapsulated and sent to the external gateway.
			checkConnectivityPingToHost(f, ciWorkerNodeSrc, "external-gateway-e2e", extGW, ipv4PingCommand, 30))
	})
})

// Validate pods can reach the initial gateway and then update the namespace
// annotation to point to a second container also emulating the external gateway
var _ = Describe("e2e multiple external gateway update validation", func() {
	const (
		svcname             string = "multiple-externalgw"
		extGwAlt1           string = "10.249.1.1"
		extGwAlt2           string = "10.249.2.1"
		ovnNs               string = "ovn-kubernetes"
		ovnWorkerNode       string = "ovn-worker"
		ovnHaWorkerNode     string = "ovn-control-plane2"
		ovnContainer        string = "ovnkube-node"
		gwContainerNameAlt1 string = "gw-test-container-alt"
		gwContainerNameAlt2 string = "gw-test-container-alt2"
		getPodIPRetry       int    = 20
	)

	var haMode bool
	ovnNsFlag := fmt.Sprintf("--namespace=%s", ovnNs)
	f := framework.NewDefaultFramework(svcname)

	// Determine what mode the CI is running in and get relevant endpoint information for the tests
	BeforeEach(func() {
		labelFlag := fmt.Sprintf("name=%s", ovnContainer)
		jsonFlag := "-o=jsonpath='{.items..metadata.name}'"
		fieldSelectorFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnWorkerNode)
		fieldSelectorHaFlag := fmt.Sprintf("--field-selector=spec.nodeName=%s", ovnHaWorkerNode)
		// start the container that will act as an external gateway
		kubectlOut, err := framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorFlag)
		if err != nil {
			framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnWorkerNode, err)
		}
		// attempt to retrieve the pod name that will source the tunnel test in HA mode
		if kubectlOut == "''" {
			haMode = true
			kubectlOut, err = framework.RunKubectl("get", "pods", ovnNsFlag, "-l", labelFlag, jsonFlag, fieldSelectorHaFlag)
			if err != nil {
				framework.Failf("Expected container %s running on %s error %v", ovnContainer, ovnHaWorkerNode, err)
			}
		}
	})

	AfterEach(func() {
		// tear down the containers simulating the gateways
		_, err := runCommand("docker", "rm", "-f", gwContainerNameAlt1)
		if err != nil {
			framework.Failf("failed to delete the gateway test container %s %v", gwContainerNameAlt1, err)
		}
		_, err = runCommand("docker", "rm", "-f", gwContainerNameAlt2)
		if err != nil {
			framework.Failf("failed to delete the gateway test container %s %v", gwContainerNameAlt2, err)
		}
	})

	It("Should validate connectivity before and after updating the namespace annotation to a new vtep and external gateway", func() {

		var pingSrc string
		var validIP net.IP
		extGWCidrAlt1 := fmt.Sprintf("%s/24", extGwAlt1)
		extGWCidrAlt2 := fmt.Sprintf("%s/24", extGwAlt2)
		srcPingPodName := "e2e-exgw-src-ping-pod"
		command := []string{"bash", "-c", "sleep 20000"}
		frameworkNsFlag := fmt.Sprintf("--namespace=%s", f.Namespace.Name)
		testContainer := fmt.Sprintf("%s-container", srcPingPodName)
		testContainerFlag := fmt.Sprintf("--container=%s", testContainer)
		// start the container that will act as an external gateway
		_, err := runCommand("docker", "run", "-itd", "--privileged", "--name", gwContainerNameAlt1, "centos")
		if err != nil {
			framework.Failf("failed to start external gateway test container %s: %v", gwContainerNameAlt1, err)
		}
		// retrieve the container ip of the external gateway container
		exVtepIpAlt1, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", gwContainerNameAlt1)
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		// trim newline from the inspect output
		exVtepIpAlt1 = strings.TrimSuffix(exVtepIpAlt1, "\n")
		if ip := net.ParseIP(exVtepIpAlt1); ip == nil {
			framework.Failf("Unable to retrieve a valid address from container %s with inspect output of %s", gwContainerNameAlt1, exVtepIpAlt1)
		}
		// annotate the test namespace
		annotateArgs := []string{
			"annotate",
			"namespace",
			f.Namespace.Name,
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-external-gw=%s", extGwAlt1),
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-vtep=%s", exVtepIpAlt1),
		}
		framework.Logf("Annotating the external gateway test namespace to a new container vtep:%s gw:%s ", exVtepIpAlt1, extGwAlt1)
		framework.RunKubectlOrDie(annotateArgs...)
		// non-ha ci mode runs a set of kind nodes prefixed with ovn-worker
		ciWorkerNodeSrc := ovnWorkerNode
		if haMode {
			// ha ci mode runs a named set of nodes with a prefix of ovn-control-plane
			ciWorkerNodeSrc = ovnHaWorkerNode
		}
		localVtepIP, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", ciWorkerNodeSrc)
		if err != nil {
			framework.Failf("failed to get the node ip address from node %s %v", ciWorkerNodeSrc, err)
		}
		localVtepIP = strings.TrimSuffix(localVtepIP, "\n")
		if ip := net.ParseIP(localVtepIP); ip == nil {
			framework.Failf("Unable to retrieve a valid address from container %s with inspect output of %s", ciWorkerNodeSrc, localVtepIP)
		}
		framework.Logf("the pod side vtep node is %s and the ip %s", ciWorkerNodeSrc, localVtepIP)
		// retrieve the pod cidr for the worker node
		jsonFlag := "jsonpath='{.metadata.annotations.k8s\\.ovn\\.org/node-subnets}'"
		kubectlOut, err := framework.RunKubectl("get", "node", ciWorkerNodeSrc, "-o", jsonFlag)
		if err != nil {
			framework.Failf("Error retrieving the pod cidr from %s %v", ciWorkerNodeSrc, err)
		}
		// strip the apostrophe from stdout and parse the pod cidr
		annotation := strings.Replace(kubectlOut, "'", "", -1)
		defaultSubnet := make(map[string]string)
		if err := json.Unmarshal([]byte(annotation), &defaultSubnet); err != nil {
			framework.Failf("Error parsing the pod cidr from %s %v", ciWorkerNodeSrc, err)
		}
		podCIDR := defaultSubnet["default"]
		framework.Logf("the pod cidr for node %s is %s", ciWorkerNodeSrc, podCIDR)
		// setup the new container to emulate a gateway with routes, vtep and a loopback interface acting as the gateway
		_, err = runCommand("docker", "exec", gwContainerNameAlt1, "ip", "link", "add", "vxlan0", "type", "vxlan", "dev",
			"eth0", "id", "4097", "dstport", vxlanPort, "remote", localVtepIP)
		if err != nil {
			framework.Failf("failed to create the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt1, "ip", "link", "set", "vxlan0", "up")
		if err != nil {
			framework.Failf("failed to enable the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt1, "ip", "address", "add", extGWCidrAlt1, "dev", "lo")
		if err != nil {
			framework.Failf("failed to add the external gateway ip to dev lo on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt1, "ip", "route", "add", podCIDR, "dev", "vxlan0")
		if err != nil {
			framework.Failf("failed to add the pod route on the test container: %v", err)
		}
		// Create the pod that will be used as the source for the connectivity test
		createGenericPod(f, srcPingPodName, ciWorkerNodeSrc, command)
		// There is a condition with e2e WaitForPodNotPending that returns ready
		// before calling for the IP address will succeed. This simply adds some retries.
		for i := 1; i < getPodIPRetry; i++ {
			pingSrc, err = getPodAddress(srcPingPodName, f.Namespace.Name)
			if err != nil {
				framework.Logf("Warning unable to query the test pod on node %s %v", ciWorkerNodeSrc, err)
			}
			validIP = net.ParseIP(pingSrc)
			if validIP != nil {
				framework.Logf("Source pod is %s is %s", srcPingPodName, pingSrc)
				break
			}
			time.Sleep(time.Second * 3)
			framework.Logf("Retry attempt %d to get pod IP from initializing pod %s", i, srcPingPodName)
		}
		// Fail the test if no address is ever retrieved
		if validIP == nil {
			framework.Failf("Warning: Failed to get an IP for the source pod %s, test will fail", srcPingPodName)
		}
		time.Sleep(time.Second * 15)
		// Verify the initial gateway is reachable from the new pod
		By(fmt.Sprintf("Verifying connectivity to the updated annotation and initial external gateway %s and vtep %s", extGwAlt1, exVtepIpAlt1))
		kubectlOut, err = framework.RunKubectl("exec", srcPingPodName, frameworkNsFlag, testContainerFlag, "--", "ping", "-w", "40", extGwAlt1)
		if err != nil {
			framework.Failf("Failed to ping the first gateway %s from container %s on node %s: %v", extGwAlt1, ovnContainer, ovnWorkerNode, err)
		}
		// start the container that will act as a new external gateway that the tests will be updated to use
		_, err = runCommand("docker", "run", "-itd", "--privileged", "--name", gwContainerNameAlt2, "centos")
		if err != nil {
			framework.Failf("failed to start external gateway test container %s: %v", gwContainerNameAlt2, err)
		}
		// retrieve the container ip of the external gateway container
		exVtepIpAlt2, err := runCommand("docker", "inspect", "-f", "{{ .NetworkSettings.IPAddress }}", gwContainerNameAlt2)
		if err != nil {
			framework.Failf("failed to start external gateway test container: %v", err)
		}
		// trim newline from the inspect output
		exVtepIpAlt2 = strings.TrimSuffix(exVtepIpAlt2, "\n")
		if ip := net.ParseIP(localVtepIP); ip == nil {
			framework.Failf("Unable to retrieve a valid address from container %s with inspect output of %s", gwContainerNameAlt2, localVtepIP)
		}
		// override the annotation in the test namespace with the new vtep and gateway
		annotateArgs = []string{
			"annotate",
			"namespace",
			f.Namespace.Name,
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-external-gw=%s", extGwAlt2),
			fmt.Sprintf("k8s.ovn.org/hybrid-overlay-vtep=%s", exVtepIpAlt2),
			"--overwrite",
		}
		framework.Logf("Annotating the external gateway test namespace to a new container vtep:%s gw:%s ", exVtepIpAlt2, extGwAlt2)
		framework.RunKubectlOrDie(annotateArgs...)
		// setup the new container to emulate a gateway with routes, vtep and a loopback interface acting as the gateway
		_, err = runCommand("docker", "exec", gwContainerNameAlt2, "ip", "link", "add", "vxlan0", "type", "vxlan", "dev",
			"eth0", "id", "4097", "dstport", vxlanPort, "remote", localVtepIP)
		if err != nil {
			framework.Failf("failed to create the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt2, "ip", "link", "set", "vxlan0", "up")
		if err != nil {
			framework.Failf("failed to enable the vxlan interface on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt2, "ip", "address", "add", extGWCidrAlt2, "dev", "lo")
		if err != nil {
			framework.Failf("failed to add the external gateway ip to dev lo on the test container: %v", err)
		}
		_, err = runCommand("docker", "exec", gwContainerNameAlt2, "ip", "route", "add", podCIDR, "dev", "vxlan0")
		if err != nil {
			framework.Failf("failed to add the pod route on the test container: %v", err)
		}
		time.Sleep(time.Second * 40)
		// Verify the updated gateway is reachable from the initial pod
		By(fmt.Sprintf("Verifying connectivity to the updated annotation and new external gateway %s and vtep %s", extGwAlt2, exVtepIpAlt2))
		kubectlOut, err = framework.RunKubectl("exec", srcPingPodName, frameworkNsFlag, testContainerFlag, "--", "ping", "-w", "40", extGwAlt2)
		if err != nil {
			framework.Failf("Failed to ping the second gateway %s from container %s on node %s: %v", extGwAlt2, ovnContainer, ovnWorkerNode, err)
		}
	})
})
