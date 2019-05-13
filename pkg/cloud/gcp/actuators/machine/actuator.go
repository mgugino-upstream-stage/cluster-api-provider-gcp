package machine

// This is a thin layer to implement the machine actuator interface with cloud provider details.
// The lifetime of scope and reconciler is a machine actuator operation.
// when scope is closed, it will persist to etcd the given machine spec and machine status (if modified)
import (
	"context"
	"fmt"

	clusterv1 "github.com/openshift/cluster-api/pkg/apis/cluster/v1alpha1"
	machinev1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	mapiclient "github.com/openshift/cluster-api/pkg/client/clientset_generated/clientset/typed/machine/v1beta1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	controllerclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	scopeFailFmt = "failed to create scope for machine %q: %v"
)

// Actuator is responsible for performing machine reconciliation.
type Actuator struct {
	machineClient mapiclient.MachineV1beta1Interface
	coreClient    controllerclient.Client
	eventRecorder record.EventRecorder
}

// ActuatorParams holds parameter information for Actuator.
type ActuatorParams struct {
	MachineClient mapiclient.MachineV1beta1Interface
	CoreClient    controllerclient.Client
	EventRecorder record.EventRecorder
}

// NewActuator returns an actuator.
func NewActuator(params ActuatorParams) *Actuator {
	return &Actuator{
		machineClient: params.MachineClient,
		coreClient:    params.CoreClient,
		eventRecorder: params.EventRecorder,
	}
}

// Create creates a machine and is invoked by the machine controller.
func (a *Actuator) Create(ctx context.Context, cluster *clusterv1.Cluster, machine *machinev1.Machine) error {
	klog.Infof("Creating machine %q", machine.Name)
	scope, err := newMachineScope(machineScopeParams{
		machineClient: a.machineClient,
		coreClient:    a.coreClient,
		machine:       machine,
	})
	if err != nil {
		return fmt.Errorf(scopeFailFmt, machine.Name, err)
	}
	defer scope.Close()
	return newReconciler(scope, a.eventRecorder).create()
}

func (a *Actuator) Exists(ctx context.Context, cluster *clusterv1.Cluster, machine *machinev1.Machine) (bool, error) {
	klog.Infof("Checking if machine %q exists", machine.Name)
	scope, err := newMachineScope(machineScopeParams{
		machineClient: a.machineClient,
		coreClient:    a.coreClient,
		machine:       machine,
	})
	if err != nil {
		return false, fmt.Errorf(scopeFailFmt, machine.Name, err)
	}
	// The core machine controller calls exists() + create()/update() in the same reconciling operation.
	// If exists() would store machineSpec/status object then create()/update() would still receive the local version.
	// When create()/update() try to store machineSpec/status this might result in
	// "Operation cannot be fulfilled; the object has been modified; please apply your changes to the latest version and try again."
	// Therefore we don't close the scope here and we only store spec/status atomically either in create()/update()"
	return newReconciler(scope, a.eventRecorder).exists()
}

func (a *Actuator) Update(ctx context.Context, cluster *clusterv1.Cluster, machine *machinev1.Machine) error {
	klog.Infof("Updating machine %q", machine.Name)
	scope, err := newMachineScope(machineScopeParams{
		machineClient: a.machineClient,
		coreClient:    a.coreClient,
		machine:       machine,
	})
	if err != nil {
		return fmt.Errorf("failed to create scope for machine %q: %v", machine.Name, err)
	}
	defer scope.Close()
	return newReconciler(scope, a.eventRecorder).update()
}

func (a *Actuator) Delete(ctx context.Context, cluster *clusterv1.Cluster, machine *machinev1.Machine) error {
	klog.Infof("Deleting machine %v", machine.Name)
	scope, err := newMachineScope(machineScopeParams{
		machineClient: a.machineClient,
		coreClient:    a.coreClient,
		machine:       machine,
	})
	if err != nil {
		return fmt.Errorf(scopeFailFmt, machine.Name, err)
	}
	return newReconciler(scope, a.eventRecorder).delete()
}
