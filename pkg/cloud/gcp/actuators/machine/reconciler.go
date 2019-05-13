package machine

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/openshift/cluster-api-provider-gcp/pkg/apis/gcpprovider/v1beta1"
	machinev1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	apierrors "github.com/openshift/cluster-api/pkg/errors"
	"google.golang.org/api/compute/v1"
	googleapi "google.golang.org/api/googleapi"
	apicorev1 "k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	userDataSecretKey  = "userData"
	operationTimeOut   = 180 * time.Second
	operationRetryWait = 5 * time.Second
	createEventAction  = "Create"
	updateEventAction  = "Update"
	deleteEventAction  = "Delete"
	noEventAction      = ""
)

// Reconciler are list of services required by machine actuator, easy to create a fake
type Reconciler struct {
	*machineScope
	eventRecorder record.EventRecorder
}

// NewReconciler populates all the services based on input scope
func newReconciler(scope *machineScope, recorder record.EventRecorder) *Reconciler {
	return &Reconciler{
		machineScope:  scope,
		eventRecorder: recorder,
	}
}

// Set corresponding event based on error. It also returns the original error
// for convenience, so callers can do "return handleMachineError(...)".
func (r *Reconciler) handleMachineError(machine *machinev1.Machine, err *apierrors.MachineError, eventAction string) error {
	if eventAction != noEventAction {
		r.eventRecorder.Eventf(machine, corev1.EventTypeWarning, "Failed"+eventAction, "%v", err.Reason)
	}

	klog.Errorf("Machine error: %v", err.Message)
	return err
}

// Create creates machine if and only if machine exists, handled by cluster-api
func (r *Reconciler) create() error {
	defer r.reconcileMachineWithCloudState()
	if err := validateMachine(*r.machine, *r.providerSpec); err != nil {
		return r.handleMachineError(r.machine, apierrors.InvalidMachineConfiguration("error decoding MachineProviderConfig: %v", err), createEventAction)
	}

	zone := r.providerSpec.Zone
	instance := &compute.Instance{
		CanIpForward:       r.providerSpec.CanIPForward,
		DeletionProtection: r.providerSpec.DeletionProtection,
		Labels:             r.providerSpec.Labels,
		MachineType:        fmt.Sprintf("zones/%s/machineTypes/%s", zone, r.providerSpec.MachineType),
		Name:               r.machine.Name,
		Tags: &compute.Tags{
			Items: r.providerSpec.Tags,
		},
	}

	// disks
	var disks = []*compute.AttachedDisk{}
	for _, disk := range r.providerSpec.Disks {
		disks = append(disks, &compute.AttachedDisk{
			AutoDelete: disk.AutoDelete,
			Boot:       disk.Boot,
			InitializeParams: &compute.AttachedDiskInitializeParams{
				DiskSizeGb:  disk.SizeGb,
				DiskType:    fmt.Sprintf("zones/%s/diskTypes/%s", zone, disk.Type),
				Labels:      disk.Labels,
				SourceImage: disk.Image,
			},
		})
	}
	instance.Disks = disks

	// networking
	var networkInterfaces = []*compute.NetworkInterface{}
	for _, nic := range r.providerSpec.NetworkInterfaces {
		computeNIC := &compute.NetworkInterface{
			AccessConfigs: []*compute.AccessConfig{{}},
		}
		if len(nic.Network) != 0 {
			computeNIC.Network = fmt.Sprintf("projects/%s/global/networks/%s", r.projectID, nic.Network)
		}
		if len(nic.Subnetwork) != 0 {
			computeNIC.Subnetwork = fmt.Sprintf("regions/%s/subnetworks/%s", r.providerSpec.Region, nic.Subnetwork)
		}
		networkInterfaces = append(networkInterfaces, computeNIC)
	}
	instance.NetworkInterfaces = networkInterfaces

	// serviceAccounts
	var serviceAccounts = []*compute.ServiceAccount{}
	for _, sa := range r.providerSpec.ServiceAccounts {
		serviceAccounts = append(serviceAccounts, &compute.ServiceAccount{
			Email:  sa.Email,
			Scopes: sa.Scopes,
		})
	}
	instance.ServiceAccounts = serviceAccounts

	// userData
	userData, err := r.getCustomUserData()
	if err != nil {
		return fmt.Errorf("error getting custom user data: %v", err)
	}
	var metadataItems = []*compute.MetadataItems{
		{
			Key:   "user-data",
			Value: &userData,
		},
	}
	for _, metadata := range r.providerSpec.Metadata {
		metadataItems = append(metadataItems, &compute.MetadataItems{
			Key:   metadata.Key,
			Value: metadata.Value,
		})
	}
	instance.Metadata = &compute.Metadata{
		Items: metadataItems,
	}

	operation, err := r.computeService.InstancesInsert(r.projectID, zone, instance)
	if err != nil {
		return fmt.Errorf("failed to create instance via compute service: %v", err)
	}
	if op, err := r.waitUntilOperationCompleted(zone, operation.Name); err != nil {
		return fmt.Errorf("failed to wait for create operation via compute service. Operation status: %v. Error: %v", op, err)
	}
	// This event is best-effort and might get missed in case of timeout
	// on waitUntilOperationCompleted
	r.eventRecorder.Eventf(r.machine, corev1.EventTypeNormal, "Created", "Created Machine %v", r.machine.Name)
	return nil
}

func (r *Reconciler) update() error {
	return r.reconcileMachineWithCloudState()
}

func (r *Reconciler) reconcileMachineWithCloudState() error {
	klog.Infof("Reconciling machine object %q with cloud state", r.machine.Name)
	freshInstance, err := r.computeService.InstancesGet(r.projectID, r.providerSpec.Zone, r.machine.Name)
	if err != nil {
		return fmt.Errorf("failed to get instance via compute service: %v", err)
	}

	if len(freshInstance.NetworkInterfaces) < 1 {
		return fmt.Errorf("could not find network interfaces for instance %q", freshInstance.Name)
	}
	networkInterface := freshInstance.NetworkInterfaces[0]

	nodeAddresses := []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: networkInterface.NetworkIP}}
	for _, config := range networkInterface.AccessConfigs {
		nodeAddresses = append(nodeAddresses, v1.NodeAddress{Type: v1.NodeExternalIP, Address: config.NatIP})
	}

	r.machine.Status.Addresses = nodeAddresses
	r.providerStatus.InstanceState = &freshInstance.Status
	r.providerStatus.InstanceID = &freshInstance.Name
	r.machine.Spec.ProviderID = &r.providerID
	return nil
}

func (r *Reconciler) getCustomUserData() (string, error) {
	if r.providerSpec.UserDataSecret == nil {
		return "", nil
	}
	var userDataSecret apicorev1.Secret

	if err := r.coreClient.Get(context.Background(), client.ObjectKey{Namespace: r.machine.GetNamespace(), Name: r.providerSpec.UserDataSecret.Name}, &userDataSecret); err != nil {
		return "", fmt.Errorf("error getting user data secret %q in namespace %q: %v", r.providerSpec.UserDataSecret.Name, r.machine.GetNamespace(), err)
	}
	data, exists := userDataSecret.Data[userDataSecretKey]
	if !exists {
		return "", fmt.Errorf("secret %v/%v does not have %q field set. Thus, no user data applied when creating an instance", r.machine.GetNamespace(), r.providerSpec.UserDataSecret.Name, userDataSecretKey)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func (r *Reconciler) waitUntilOperationCompleted(zone, operationName string) (*compute.Operation, error) {
	var op *compute.Operation
	var err error
	return op, wait.Poll(operationRetryWait, operationTimeOut, func() (bool, error) {
		op, err = r.computeService.ZoneOperationsGet(r.projectID, zone, operationName)
		if err != nil {
			return false, err
		}
		klog.V(3).Infof("Waiting for %q operation to be completed... (status: %s)", op.OperationType, op.Status)
		if op.Status == "DONE" {
			if op.Error == nil {
				return true, nil
			}
			var err []error
			for _, opErr := range op.Error.Errors {
				err = append(err, fmt.Errorf("%s", *opErr))
			}
			return false, fmt.Errorf("the following errors occurred: %+v", err)
		}
		return false, nil
	})
}

func validateMachine(machine machinev1.Machine, providerSpec v1beta1.GCPMachineProviderSpec) error {
	// TODO (alberto): First validation should happen via webhook before the object is persisted.
	// This is a complementary validation to fail early in case of lacking proper webhook validation.
	// Default values can also be set here
	return nil
}

// Returns true if machine exists.
func (r *Reconciler) exists() (bool, error) {
	if err := validateMachine(*r.machine, *r.providerSpec); err != nil {
		return false, fmt.Errorf("failed validating machine provider spec: %v", err)
	}
	zone := r.providerSpec.Zone
	// Need to verify that our project/zone exists before checking machine, as
	// invalid project/zone produces same 404 error as no machine.
	if err := r.validateZone(); err != nil {
		return false, fmt.Errorf("unable to verify project/zone exists: %v/%v; err: %v", r.projectID, zone, err)
	}
	_, err := r.computeService.InstancesGet(r.projectID, zone, r.machine.Name)
	if err == nil {
		klog.Infof("Machine %q already exists", r.machine.Name)
		return true, nil
	}
	if isNotFoundError(err) {
		klog.Infof("Machine %q does not exist", r.machine.Name)
		return false, nil
	}
	return false, fmt.Errorf("error getting running instances: %v", err)
}

// Returns true if machine exists.
func (r *Reconciler) delete() error {
	exists, err := r.exists()
	if err != nil {
		return err
	}
	if !exists {
		klog.Infof("Machine %v not found during delete, skipping", r.machine.Name)
		return nil
	}
	zone := r.providerSpec.Zone
	operation, err := r.computeService.InstancesDelete(r.projectID, zone, r.machine.Name)
	if err != nil {
		return fmt.Errorf("failed to delete instance via compute service: %v", err)
	}
	if op, err := r.waitUntilOperationCompleted(zone, operation.Name); err != nil {
		return fmt.Errorf("failed to wait for delete operation via compute service. Operation status: %v. Error: %v", op, err)
	}
	return nil
}

func (r *Reconciler) validateZone() error {
	_, err := r.computeService.ZonesGet(r.projectID, r.providerSpec.Zone)
	return err
}

func isNotFoundError(err error) bool {
	switch t := err.(type) {
	case *googleapi.Error:
		return t.Code == 404
	}
	return false
}
