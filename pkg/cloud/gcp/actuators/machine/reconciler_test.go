package machine

import (
	"fmt"
	"testing"

	gcpv1beta1 "github.com/openshift/cluster-api-provider-gcp/pkg/apis/gcpprovider/v1beta1"
	computeservice "github.com/openshift/cluster-api-provider-gcp/pkg/cloud/gcp/actuators/services/compute"
	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	controllerfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreate(t *testing.T) {
	_, mockComputeService := computeservice.NewComputeServiceMock()
	machineScope := machineScope{
		machine: &machinev1beta1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "",
				Namespace: "",
			},
		},
		coreClient:     controllerfake.NewFakeClient(),
		providerSpec:   &gcpv1beta1.GCPMachineProviderSpec{},
		providerStatus: &gcpv1beta1.GCPMachineProviderStatus{},
		computeService: mockComputeService,
	}
	eventsChannel := make(chan string, 1)
	recorder := &record.FakeRecorder{
		Events: eventsChannel,
	}
	reconciler := newReconciler(&machineScope, recorder)
	if err := reconciler.create(); err != nil {
		t.Errorf("reconciler was not expected to return error: %v", err)
	}
}

func TestReconcileMachineWithCloudState(t *testing.T) {
	_, mockComputeService := computeservice.NewComputeServiceMock()

	zone := "us-east1-b"
	projecID := "testProject"
	instanceName := "testInstance"
	machineScope := machineScope{
		machine: &machinev1beta1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: "",
			},
		},
		coreClient: controllerfake.NewFakeClient(),
		providerSpec: &gcpv1beta1.GCPMachineProviderSpec{
			Zone: zone,
		},
		projectID:      projecID,
		providerID:     fmt.Sprintf("gce://%s/%s/%s", projecID, zone, instanceName),
		providerStatus: &gcpv1beta1.GCPMachineProviderStatus{},
		computeService: mockComputeService,
	}

	expectedNodeAddresses := []corev1.NodeAddress{
		{
			Type:    "InternalIP",
			Address: "10.0.0.15",
		},
		{
			Type:    "ExternalIP",
			Address: "35.243.147.143",
		},
	}

	eventsChannel := make(chan string, 1)
	recorder := &record.FakeRecorder{
		Events: eventsChannel,
	}
	r := newReconciler(&machineScope, recorder)
	if err := r.reconcileMachineWithCloudState(); err != nil {
		t.Errorf("reconciler was not expected to return error: %v", err)
	}
	if r.machine.Status.Addresses[0] != expectedNodeAddresses[0] {
		t.Errorf("Expected: %s, got: %s", expectedNodeAddresses[0], r.machine.Status.Addresses[0])
	}
	if r.machine.Status.Addresses[1] != expectedNodeAddresses[1] {
		t.Errorf("Expected: %s, got: %s", expectedNodeAddresses[1], r.machine.Status.Addresses[1])
	}

	if r.providerID != *r.machine.Spec.ProviderID {
		t.Errorf("Expected: %s, got: %s", r.providerID, *r.machine.Spec.ProviderID)
	}
	if *r.providerStatus.InstanceState != "RUNNING" {
		t.Errorf("Expected: %s, got: %s", "RUNNING", *r.providerStatus.InstanceState)
	}
	if *r.providerStatus.InstanceID != instanceName {
		t.Errorf("Expected: %s, got: %s", instanceName, *r.providerStatus.InstanceID)
	}
}

func TestExists(t *testing.T) {
	_, mockComputeService := computeservice.NewComputeServiceMock()
	machineScope := machineScope{
		machine: &machinev1beta1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "",
				Namespace: "",
			},
		},
		coreClient:     controllerfake.NewFakeClient(),
		providerSpec:   &gcpv1beta1.GCPMachineProviderSpec{},
		providerStatus: &gcpv1beta1.GCPMachineProviderStatus{},
		computeService: mockComputeService,
	}
	eventsChannel := make(chan string, 1)
	recorder := &record.FakeRecorder{
		Events: eventsChannel,
	}
	reconciler := newReconciler(&machineScope, recorder)
	exists, err := reconciler.exists()
	if err != nil || exists != true {
		t.Errorf("reconciler was not expected to return error: %v", err)
	}
}

func TestDelete(t *testing.T) {
	_, mockComputeService := computeservice.NewComputeServiceMock()
	machineScope := machineScope{
		machine: &machinev1beta1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "",
				Namespace: "",
			},
		},
		coreClient:     controllerfake.NewFakeClient(),
		providerSpec:   &gcpv1beta1.GCPMachineProviderSpec{},
		providerStatus: &gcpv1beta1.GCPMachineProviderStatus{},
		computeService: mockComputeService,
	}
	eventsChannel := make(chan string, 1)
	recorder := &record.FakeRecorder{
		Events: eventsChannel,
	}
	reconciler := newReconciler(&machineScope, recorder)
	if err := reconciler.delete(); err != nil {
		t.Errorf("reconciler was not expected to return error: %v", err)
	}
}
