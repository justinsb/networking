package routing

import (
	"encoding/json"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	"kope.io/networking/pkg/cni"
)

// Controller updates the routing provider, if any changes have been made
type Controller struct {
	nodeMap         *NodeMap
	provider        Provider
	kubeClient      kubernetes.Interface
	cniConfigWriter cni.ConfigWriter
}

// NewController creates a routing.Controller
func NewController(kubeClient kubernetes.Interface, nodeMap *NodeMap, provider Provider, cniConfigWriter cni.ConfigWriter) (*Controller, error) {
	c := &Controller{
		kubeClient:      kubeClient,
		nodeMap:         nodeMap,
		provider:        provider,
		cniConfigWriter: cniConfigWriter,
	}

	return c, nil
}

// Run starts the NodeController.
func (c *Controller) Run() {
	klog.Infof("starting node controller")

	go c.runWatcher()
}

func (c *Controller) runWatcher() {
	for {
		if c.nodeMap.IsReady() {
			break
		}
		klog.Infof("node map not yet ready")
		time.Sleep(1 * time.Second)
	}
	klog.Infof("node map is ready")
	for {
		err := c.provider.EnsureCIDRs(c.nodeMap)
		if err != nil {
			klog.Warningf("Unexpected error in provider controller, will retry: %v", err)
			time.Sleep(10 * time.Second)
			continue
		} else {
			time.Sleep(1 * time.Second)
		}

		if c.cniConfigWriter != nil && c.nodeMap.me != nil {
			if err := c.cniConfigWriter.WriteCNIConfig(c.nodeMap.me.PodCIDR); err != nil {
				klog.Warningf("unexpected error writing CNI config, will retry: %v", err)
				time.Sleep(10 * time.Second)
				continue
			}
		}

		if c.nodeMap.me != nil && !c.nodeMap.me.NetworkAvailable {
			nodeName := c.nodeMap.me.Name
			klog.Infof("marking node %q as network-ready in node status", nodeName)
			currentTime := metav1.Now()
			err = setNodeCondition(c.kubeClient, nodeName, v1.NodeCondition{
				Type:               v1.NodeNetworkUnavailable,
				Status:             v1.ConditionFalse,
				Reason:             "RouteCreated",
				Message:            "kope.io network controller initialized node routes",
				LastTransitionTime: currentTime,
			})
			if err != nil {
				// Very small chance of conflict
				if !errors.IsConflict(err) {
					klog.Errorf("Error updating node %s: %v", nodeName, err)
				}
				klog.Errorf("Error updating node %s, will retry: %v", nodeName, err)
			}

		}
	}
}

// Borrowed from k8s.io/kubernetes/pkg/util/node/node.go

// SetNodeCondition updates specific node condition with patch operation.
func setNodeCondition(c kubernetes.Interface, node string, condition v1.NodeCondition) error {
	generatePatch := func(condition v1.NodeCondition) ([]byte, error) {
		raw, err := json.Marshal(&[]v1.NodeCondition{condition})
		if err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf(`{"status":{"conditions":%s}}`, raw)), nil
	}
	condition.LastHeartbeatTime = metav1.Now()
	patch, err := generatePatch(condition)
	if err != nil {
		return nil
	}
	_, err = c.CoreV1().Nodes().PatchStatus(string(node), patch)
	return err
}
