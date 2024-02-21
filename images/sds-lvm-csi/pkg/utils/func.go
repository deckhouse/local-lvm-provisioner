/*
Copyright 2024 Flant JSC

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

package utils

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sds-lvm-csi/api/v1alpha1"
	"sds-lvm-csi/pkg/logger"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LLVStatusCreated            = "Created"
	LLVStatusFailed             = "Failed"
	LLVTypeThin                 = "Thin"
	KubernetesApiRequestLimit   = 3
	KubernetesApiRequestTimeout = 1
)

func CreateLVMLogicalVolume(ctx context.Context, kc client.Client, name string, LVMLogicalVolumeSpec v1alpha1.LVMLogicalVolumeSpec) (*v1alpha1.LVMLogicalVolume, error) {
	var err error
	llv := &v1alpha1.LVMLogicalVolume{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMLogicalVolumeKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{},
		},
		Spec: LVMLogicalVolumeSpec,
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Create(ctx, llv)
		if err == nil {
			return llv, nil
		}

		if kerrors.IsAlreadyExists(err) {
			return nil, err
		}

		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return nil, fmt.Errorf("after %d attempts of creating LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, name, err)
	}
	return llv, nil
}

func DeleteLVMLogicalVolume(ctx context.Context, kc client.Client, LVMLogicalVolumeName string) error {
	var err error
	llv := &v1alpha1.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: LVMLogicalVolumeName,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMLogicalVolumeKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err := kc.Delete(ctx, llv)
		if err == nil {
			return nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return fmt.Errorf("after %d attempts of deleting LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, LVMLogicalVolumeName, err)
	}
	return nil
}

func WaitForStatusUpdate(ctx context.Context, kc client.Client, log logger.Logger, LVMLogicalVolumeName, namespace string, llvSize, delta resource.Quantity) (int, error) {
	var attemptCounter int
	sizeEquals := false
	for {
		attemptCounter++
		select {
		case <-ctx.Done():
			return attemptCounter, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		llv, err := GetLVMLogicalVolume(ctx, kc, LVMLogicalVolumeName, namespace)
		if err != nil {
			return attemptCounter, err
		}

		if attemptCounter%10 == 0 {
			log.Info(fmt.Sprintf("[WaitForStatusUpdate] Attempt: %d,LVM Logical Volume: %+v; delta=%s; sizeEquals=%t", attemptCounter, llv, delta.String(), sizeEquals))
		}

		if llv.Status != nil {
			sizeEquals = AreSizesEqualWithinDelta(llvSize, llv.Status.ActualSize, delta)

			if llv.Status.Phase == LLVStatusFailed {
				return attemptCounter, fmt.Errorf("LVM Logical Volume %s failed, reason: %s", LVMLogicalVolumeName, llv.Status.Reason)
			}

			if llv.Status.Phase == LLVStatusCreated && sizeEquals {
				return attemptCounter, nil
			}
		}

	}
}

func GetLVMLogicalVolume(ctx context.Context, kc client.Client, LVMLogicalVolumeName, namespace string) (*v1alpha1.LVMLogicalVolume, error) {
	var llv v1alpha1.LVMLogicalVolume
	var err error

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      LVMLogicalVolumeName,
			Namespace: namespace,
		}, &llv)

		if err == nil {
			return &llv, nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	return nil, fmt.Errorf("after %d attempts of getting LVMLogicalVolume %s in namespace %s, last error: %w", KubernetesApiRequestLimit, LVMLogicalVolumeName, namespace, err)
}

func AreSizesEqualWithinDelta(leftSize, rightSize, allowedDelta resource.Quantity) bool {
	leftSizeFloat := float64(leftSize.Value())
	rightSizeFloat := float64(rightSize.Value())

	return math.Abs(leftSizeFloat-rightSizeFloat) < float64(allowedDelta.Value())
}

func GetNodeMaxFreeVGSize(ctx context.Context, kc client.Client) (nodeName string, freeSpace resource.Quantity, err error) {
	listLvgs := &v1alpha1.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMVolumeGroupKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []v1alpha1.LvmVolumeGroup{},
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			break
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return "", freeSpace, fmt.Errorf("after %d attempts of getting LvmVolumeGroups, last error: %w", KubernetesApiRequestLimit, err)
	}

	var maxFreeSpace int64
	for _, lvg := range listLvgs.Items {
		freeSpace, err := GetLVMVolumeGroupFreeSpace(lvg)
		if err != nil {
			return "", freeSpace, fmt.Errorf("get free space for lvg %s: %w", lvg.Name, err)
		}

		if freeSpace.Value() > maxFreeSpace {
			nodeName = lvg.Status.Nodes[0].Name
			maxFreeSpace = freeSpace.Value()
		}
	}

	return nodeName, *resource.NewQuantity(maxFreeSpace, resource.BinarySI), nil
}

func GetLVMVolumeGroupParams(ctx context.Context, kc client.Client, log logger.Logger, lvmVG map[string]string, nodeName, LvmType string) (lvgName, vgName string, err error) {
	listLvgs := &v1alpha1.LvmVolumeGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.LVMVolumeGroupKind,
			APIVersion: v1alpha1.TypeMediaAPIVersion,
		},
		ListMeta: metav1.ListMeta{},
		Items:    []v1alpha1.LvmVolumeGroup{},
	}

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.List(ctx, listLvgs)
		if err == nil {
			break
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	if err != nil {
		return "", "", fmt.Errorf("after %d attempts of getting LvmVolumeGroups, last error: %w", KubernetesApiRequestLimit, err)
	}

	for _, lvg := range listLvgs.Items {

		log.Trace(fmt.Sprintf("[GetLVMVolumeGroupParams] process lvg: %+v", lvg))

		_, ok := lvmVG[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] lvg.Status.Nodes[0].Name: %s, prefferedNode: %s", lvg.Status.Nodes[0].Name, nodeName))
			if lvg.Status.Nodes[0].Name == nodeName {
				if LvmType == LLVTypeThin {
					for _, thinPool := range lvg.Status.ThinPools {
						for _, tp := range lvmVG {
							if thinPool.Name == tp {
								return lvg.Name, lvg.Spec.ActualVGNameOnTheNode, nil
							}
						}
					}
				}
				return lvg.Name, lvg.Spec.ActualVGNameOnTheNode, nil
			}
		} else {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] skip lvg: %s", lvg.Name))
		}

	}
	return "", "", errors.New("there are no matches")
}

func GetLVMVolumeGroup(ctx context.Context, kc client.Client, lvgName, namespace string) (*v1alpha1.LvmVolumeGroup, error) {
	var lvg v1alpha1.LvmVolumeGroup
	var err error

	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Get(ctx, client.ObjectKey{
			Name:      lvgName,
			Namespace: namespace,
		}, &lvg)

		if err == nil {
			return &lvg, nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}
	return nil, fmt.Errorf("after %d attempts of getting LvmVolumeGroup %s in namespace %s, last error: %w", KubernetesApiRequestLimit, lvgName, namespace, err)
}

func GetLVMVolumeGroupFreeSpace(lvg v1alpha1.LvmVolumeGroup) (vgFreeSpace resource.Quantity, err error) {
	vgSize, err := resource.ParseQuantity(lvg.Status.VGSize)
	if err != nil {
		return vgFreeSpace, fmt.Errorf("parse size vgSize (%s): %w", lvg.Status.VGSize, err)
	}

	allocatedSize, err := resource.ParseQuantity(lvg.Status.AllocatedSize)
	if err != nil {
		return vgFreeSpace, fmt.Errorf("parse size vgSize (%s): %w", lvg.Status.AllocatedSize, err)
	}

	vgFreeSpace = vgSize
	vgFreeSpace.Sub(allocatedSize)
	return vgFreeSpace, nil
}

func UpdateLVMLogicalVolume(ctx context.Context, kc client.Client, llv *v1alpha1.LVMLogicalVolume) error {
	var err error
	for attempt := 0; attempt < KubernetesApiRequestLimit; attempt++ {
		err = kc.Update(ctx, llv)
		if err == nil {
			return nil
		}
		time.Sleep(KubernetesApiRequestTimeout)
	}

	if err != nil {
		return fmt.Errorf("after %d attempts of updating LVMLogicalVolume %s, last error: %w", KubernetesApiRequestLimit, llv.Name, err)
	}
	return nil
}
