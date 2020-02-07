/*
Copyright 2017 The Kubernetes Authors.

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

package controller

import (
	"fmt"
	"time"
	"net/http"
	"encoding/json"
    "bytes"

	// appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	// metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	// appsinformers "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	// typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	// appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"

	samplev1alpha1 "k8s.io/sample-controller/pkg/apis/samplecontroller/v1alpha1"
	clientset "k8s.io/sample-controller/pkg/generated/clientset/versioned"
	samplescheme "k8s.io/sample-controller/pkg/generated/clientset/versioned/scheme"
	informers "k8s.io/sample-controller/pkg/generated/informers/externalversions/samplecontroller/v1alpha1"
	listers "k8s.io/sample-controller/pkg/generated/listers/samplecontroller/v1alpha1"
)

const controllerAgentName = "sample-controller"

const (
	// SuccessSynced is used as part of the Event 'reason' when a Vm is synced
	SuccessSynced = "Synced"
	// ErrResourceExists is used as part of the Event 'reason' when a Vm fails
	// to sync due to a Deployment of the same name already existing.
	ErrResourceExists = "ErrResourceExists"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by Vm"
	// MessageResourceSynced is the message used for an Event fired when a Vm
	// is synced successfully
	MessageResourceSynced = "Vm synced successfully"
)

// Controller is the controller implementation for Vm resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// sampleclientset is a clientset for our own API group
	sampleclientset clientset.Interface
	vmsLister        listers.VmLister
	vmsSynced        cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	Workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

type ApiResponse struct {
    Success    bool   `json:"success"`
    ResultCode int    `json:"result_code"`
    ResultMsg  string `json:"result_msg"`
	VmName string `json:"name"`
	VmId string `json:"id"`
	CpuUtilization int32 `json:"cpuUtilization"`
}

type RateLimitingType struct {
	DelayingInterface workqueue.DelayingInterface

	rateLimiter workqueue.RateLimiter
}

// NewController returns a new sample controller
func NewController(
	// kubeclientset kubernetes.Interface,
	sampleclientset clientset.Interface,
	vmInformer informers.VmInformer) *Controller {

	// Create event broadcaster
	// Add sample-controller types to the default Kubernetes Scheme so Events can be
	// logged for sample-controller types.
	utilruntime.Must(samplescheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	// eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		// kubeclientset:     kubeclientset,
		sampleclientset:   sampleclientset,
		vmsLister:        vmInformer.Lister(),
		vmsSynced:        vmInformer.Informer().HasSynced,
		Workqueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Vms"),
		recorder:          recorder,
	}

	klog.Info("Setting up event handlers")
	// Set up an event handler for when Vm resources change
	vmInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueVm,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueVm(new)
		},
		DeleteFunc: controller.deleteObject,
	})
	// Set up an event handler for when Deployment resources change. This
	// handler will lookup the owner of the given Deployment, and if it is
	// owned by a Vm resource will enqueue that Vm resource for
	// processing. This way, we don't need to implement custom logic for
	// handling Deployment resources. More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md
	// deploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
	// 	AddFunc: controller.handleObject,
	// 	UpdateFunc: func(old, new interface{}) {
	// 		newDepl := new.(*appsv1.Deployment)
	// 		oldDepl := old.(*appsv1.Deployment)
	// 		if newDepl.ResourceVersion == oldDepl.ResourceVersion {
	// 			// Periodic resync will send update events for all known Deployments.
	// 			// Two different versions of the same Deployment will always have different RVs.
	// 			return
	// 		}
	// 		controller.handleObject(new)
	// 	},
	// 	DeleteFunc: controller.handleObject,
	// })

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(stopCh <-chan struct{}, threadiness int) error {
	defer utilruntime.HandleCrash()
	defer c.Workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting Vm controller")

	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.vmsSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Vm resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.Workqueue.Get()
	// queueMetrics := 
	if t, ok := c.Workqueue.(*RateLimitingInterface); ok {
		fmt.Println("implements I", t)
	}
	// fmt.Printf("%+v\n", queueMetrics.DelayingInterface)

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.Workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.Workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Vm resource to be synced.
		if err := c.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.Workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.Workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Vm resource
// with the current status of the resource.
func (c *Controller) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the Vm resource with this namespace/name
	vm, err := c.vmsLister.Vms(namespace).Get(name)
	if err != nil {
		// The Vm resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("vm '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	vmName := vm.Spec.VmName
	if vmName == "" {
		utilruntime.HandleError(fmt.Errorf("%s: vm name must be specified", key))
		return nil
	}
	
	vmId := vm.Status.VmId
	cpuUtilization := vm.Status.CpuUtilization
	
	// create vm and update uuid if the uuid does not exist
	// update vm name if the uuid exists
	resp_pre, err_pre := http.Get("/servers/" + vmId)
	if err_pre != nil {
		_, err := http.Get("/check/" + vmName)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("%s: vm name %s is prohibited", err, vmName))
			return nil
		} else {
			jsonData := map[string]string{"name": vmName}
			jsonValue, _ := json.Marshal(jsonData)
			resp_cre, err := http.Post("/servers/", "application/json", bytes.NewBuffer(jsonValue))
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("%s: create vm %s failed", err, vmName))
				return nil
			} else {
				var response ApiResponse
				err := json.NewDecoder(resp_cre.Body).Decode(&response)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("%s: dump response failed", err))
				}
				vmId = response.VmId
				// fmt.Printf("%+v\n", response.VmId)
			}
		}
	} else {
		var response ApiResponse
		err := json.NewDecoder(resp_pre.Body).Decode(&response)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("%s: dump response failed", err))
		}
		vmName = response.VmName
	}

	// Get vm status
	resp_sta, err := http.Get("/servers/" + vmId + "/status")
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: get vm %s status failed", err, vmId))
		return nil
	} else {
		var response ApiResponse
		err := json.NewDecoder(resp_sta.Body).Decode(&response)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("%s: dump response failed", err))
		}
		cpuUtilization = response.CpuUtilization
	}

	// Finally, we update the status block of the Vm resource to reflect the
	// current state of the world
	err = c.updateVmStatus(vm, vmName, vmId, cpuUtilization)
	if err != nil {
		return err
	}

	c.recorder.Event(vm, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	return nil
}

func (c *Controller) updateVmStatus(vm *samplev1alpha1.Vm, vmName string, vmId string, cpuUtilization int32) error {
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	vmCopy := vm.DeepCopy()
	vmCopy.Spec.VmName = vmName
	vmCopy.Status.VmId = vmId
	vmCopy.Status.CpuUtilization = cpuUtilization
	// vmCopy.Status.AvailableReplicas = deployment.Status.AvailableReplicas
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the Vm resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err := c.sampleclientset.SamplecontrollerV1alpha1().Vms(vm.Namespace).Update(vmCopy)
	return err
}

// enqueueVm takes a Vm resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than Vm.
func (c *Controller) enqueueVm(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.Workqueue.Add(key)
}

func (c *Controller) deleteObject(obj interface{}) {
	vm := obj.(*samplev1alpha1.Vm)
	vmId := vm.Status.VmId
	_, err := http.NewRequest("DELETE", "/servers/" + vmId, nil)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: delete vm failed", err))
	}
}
