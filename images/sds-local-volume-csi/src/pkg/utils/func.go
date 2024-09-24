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
	"slices"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	snc "github.com/deckhouse/sds-node-configurator/api/v1alpha1"
	"gopkg.in/yaml.v2"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sds-local-volume-csi/internal"
	"sds-local-volume-csi/pkg/logger"
)

const (
	LLVStatusCreated            = "Created"
	LLVStatusFailed             = "Failed"
	LLVTypeThin                 = "Thin"
	KubernetesAPIRequestLimit   = 3
	KubernetesAPIRequestTimeout = 1
	SDSLocalVolumeCSIFinalizer  = "storage.deckhouse.io/sds-local-volume-csi"
)

func CreateLVMLogicalVolume(ctx context.Context, kc client.Client, log *logger.Logger, traceID, name string, lvmLogicalVolumeSpec snc.LVMLogicalVolumeSpec) (*snc.LVMLogicalVolume, error) {
	var err error
	llv := &snc.LVMLogicalVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{},
			Finalizers:      []string{SDSLocalVolumeCSIFinalizer},
		},
		Spec: lvmLogicalVolumeSpec,
	}

	log.Trace(fmt.Sprintf("[CreateLVMLogicalVolume][traceID:%s][volumeID:%s] LVMLogicalVolume: %+v", traceID, name, llv))

	err = kc.Create(ctx, llv)
	return llv, err
}

func DeleteLVMLogicalVolume(ctx context.Context, kc client.Client, log *logger.Logger, traceID, lvmLogicalVolumeName string) error {
	var err error

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Trying to find LVMLogicalVolume", traceID, lvmLogicalVolumeName))
	llv, err := GetLVMLogicalVolume(ctx, kc, lvmLogicalVolumeName, "")
	if err != nil {
		return fmt.Errorf("get LVMLogicalVolume %s: %w", lvmLogicalVolumeName, err)
	}

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] LVMLogicalVolume found: %+v (status: %+v)", traceID, lvmLogicalVolumeName, llv, llv.Status))
	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Removing finalizer %s if exists", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer))

	removed, err := removeLLVFinalizerIfExist(ctx, kc, log, llv, SDSLocalVolumeCSIFinalizer)
	if err != nil {
		return fmt.Errorf("remove finalizers from LVMLogicalVolume %s: %w", lvmLogicalVolumeName, err)
	}
	if removed {
		log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] finalizer %s removed from LVMLogicalVolume %s", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer, lvmLogicalVolumeName))
	} else {
		log.Warning(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] finalizer %s not found in LVMLogicalVolume %s", traceID, lvmLogicalVolumeName, SDSLocalVolumeCSIFinalizer, lvmLogicalVolumeName))
	}

	log.Trace(fmt.Sprintf("[DeleteLVMLogicalVolume][traceID:%s][volumeID:%s] Trying to delete LVMLogicalVolume", traceID, lvmLogicalVolumeName))
	err = kc.Delete(ctx, llv)
	return err
}

func WaitForStatusUpdate(ctx context.Context, kc client.Client, log *logger.Logger, traceID, lvmLogicalVolumeName, namespace string, llvSize, delta resource.Quantity) (int, error) {
	var attemptCounter int
	sizeEquals := false
	log.Info(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Waiting for LVM Logical Volume status update", traceID, lvmLogicalVolumeName))
	for {
		attemptCounter++
		select {
		case <-ctx.Done():
			log.Warning(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] context done. Failed to wait for LVM Logical Volume status update", traceID, lvmLogicalVolumeName))
			return attemptCounter, ctx.Err()
		default:
			time.Sleep(500 * time.Millisecond)
		}

		llv, err := GetLVMLogicalVolume(ctx, kc, lvmLogicalVolumeName, namespace)
		if err != nil {
			return attemptCounter, err
		}

		if attemptCounter%10 == 0 {
			log.Info(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt: %d,LVM Logical Volume: %+v; delta=%s; sizeEquals=%t", traceID, lvmLogicalVolumeName, attemptCounter, llv, delta.String(), sizeEquals))
		}

		if llv.Status != nil {
			log.Trace(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt %d, LVM Logical Volume status: %+v, full LVMLogicalVolume resource: %+v", traceID, lvmLogicalVolumeName, attemptCounter, llv.Status, llv))
			sizeEquals = AreSizesEqualWithinDelta(llvSize, llv.Status.ActualSize, delta)

			if llv.DeletionTimestamp != nil {
				return attemptCounter, fmt.Errorf("failed to create LVM logical volume on node for LVMLogicalVolume %s, reason: LVMLogicalVolume is being deleted", lvmLogicalVolumeName)
			}

			if llv.Status.Phase == LLVStatusFailed {
				return attemptCounter, fmt.Errorf("failed to create LVM logical volume on node for LVMLogicalVolume %s, reason: %s", lvmLogicalVolumeName, llv.Status.Reason)
			}

			if llv.Status.Phase == LLVStatusCreated {
				if sizeEquals {
					return attemptCounter, nil
				}
				log.Trace(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt %d, LVM Logical Volume created but size does not match the requested size yet. Waiting...", traceID, lvmLogicalVolumeName, attemptCounter))
			} else {
				log.Trace(fmt.Sprintf("[WaitForStatusUpdate][traceID:%s][volumeID:%s] Attempt %d, LVM Logical Volume status is not 'Created' yet. Waiting...", traceID, lvmLogicalVolumeName, attemptCounter))
			}
		}
	}
}

func GetLVMLogicalVolume(ctx context.Context, kc client.Client, lvmLogicalVolumeName, namespace string) (*snc.LVMLogicalVolume, error) {
	var llv snc.LVMLogicalVolume

	err := kc.Get(ctx, client.ObjectKey{
		Name:      lvmLogicalVolumeName,
		Namespace: namespace,
	}, &llv)

	return &llv, err
}

func AreSizesEqualWithinDelta(leftSize, rightSize, allowedDelta resource.Quantity) bool {
	leftSizeFloat := float64(leftSize.Value())
	rightSizeFloat := float64(rightSize.Value())

	return math.Abs(leftSizeFloat-rightSizeFloat) < float64(allowedDelta.Value())
}

func GetNodeWithMaxFreeSpace(lvgs []snc.LVMVolumeGroup, storageClassLVGParametersMap map[string]string, lvmType string) (nodeName string, freeSpace resource.Quantity, err error) {
	var maxFreeSpace int64
	for _, lvg := range lvgs {
		switch lvmType {
		case internal.LVMTypeThick:
			freeSpace = lvg.Status.VGFree
		case internal.LVMTypeThin:
			thinPoolName, ok := storageClassLVGParametersMap[lvg.Name]
			if !ok {
				return "", freeSpace, fmt.Errorf("thin pool name for lvg %s not found in storage class parameters: %+v", lvg.Name, storageClassLVGParametersMap)
			}
			freeSpace, err = GetLVMThinPoolFreeSpace(lvg, thinPoolName)
			if err != nil {
				return "", freeSpace, fmt.Errorf("get free space for thin pool %s in lvg %s: %w", thinPoolName, lvg.Name, err)
			}
		}

		if freeSpace.Value() > maxFreeSpace {
			nodeName = lvg.Status.Nodes[0].Name
			maxFreeSpace = freeSpace.Value()
		}
	}

	return nodeName, *resource.NewQuantity(maxFreeSpace, resource.BinarySI), nil
}

// TODO: delete the method below?
func GetLVMVolumeGroupParams(ctx context.Context, kc client.Client, log logger.Logger, lvmVG map[string]string, nodeName, lvmType string) (lvgName, vgName string, err error) {
	listLvgs := &snc.LVMVolumeGroupList{
		ListMeta: metav1.ListMeta{},
		Items:    []snc.LVMVolumeGroup{},
	}

	err = kc.List(ctx, listLvgs)
	if err != nil {
		return "", "", fmt.Errorf("error getting LVMVolumeGroup list: %w", err)
	}

	for _, lvg := range listLvgs.Items {
		log.Trace(fmt.Sprintf("[GetLVMVolumeGroupParams] process lvg: %+v", lvg))

		_, ok := lvmVG[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetLVMVolumeGroupParams] lvg.Status.Nodes[0].Name: %s, prefferedNode: %s", lvg.Status.Nodes[0].Name, nodeName))
			if lvg.Status.Nodes[0].Name == nodeName {
				if lvmType == LLVTypeThin {
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

func GetLVMVolumeGroup(ctx context.Context, kc client.Client, lvgName, namespace string) (*snc.LVMVolumeGroup, error) {
	var lvg snc.LVMVolumeGroup

	err := kc.Get(ctx, client.ObjectKey{
		Name:      lvgName,
		Namespace: namespace,
	}, &lvg)

	return &lvg, err
}

func GetLVMVolumeGroupFreeSpace(lvg snc.LVMVolumeGroup) (vgFreeSpace resource.Quantity) {
	vgFreeSpace = lvg.Status.VGSize
	vgFreeSpace.Sub(lvg.Status.AllocatedSize)
	return vgFreeSpace
}

func GetLVMThinPoolFreeSpace(lvg snc.LVMVolumeGroup, thinPoolName string) (thinPoolFreeSpace resource.Quantity, err error) {
	var storagePoolThinPool *snc.LVMVolumeGroupThinPoolStatus
	for _, thinPool := range lvg.Status.ThinPools {
		if thinPool.Name == thinPoolName {
			storagePoolThinPool = &thinPool
			break
		}
	}

	if storagePoolThinPool == nil {
		return thinPoolFreeSpace, fmt.Errorf("[GetLVMThinPoolFreeSpace] thin pool %s not found in lvg %+v", thinPoolName, lvg)
	}

	return storagePoolThinPool.AvailableSpace, nil
}

func ExpandLVMLogicalVolume(ctx context.Context, kc client.Client, llv *snc.LVMLogicalVolume, newSize string) error {
	llv.Spec.Size = newSize
	return kc.Update(ctx, llv)
}

func GetStorageClassLVGsAndParameters(ctx context.Context, kc client.Client, log *logger.Logger, storageClassLVGParametersString string) (storageClassLVGs []snc.LVMVolumeGroup, storageClassLVGParametersMap map[string]string, err error) {
	var storageClassLVGParametersList LVMVolumeGroups
	err = yaml.Unmarshal([]byte(storageClassLVGParametersString), &storageClassLVGParametersList)
	if err != nil {
		log.Error(err, "unmarshal yaml lvmVolumeGroup")
		return nil, nil, err
	}

	storageClassLVGParametersMap = make(map[string]string, len(storageClassLVGParametersList))
	for _, v := range storageClassLVGParametersList {
		storageClassLVGParametersMap[v.Name] = v.Thin.PoolName
	}
	log.Info(fmt.Sprintf("[GetStorageClassLVGs] StorageClass LVM volume groups parameters map: %+v", storageClassLVGParametersMap))

	lvgs, err := GetLVGList(ctx, kc)
	if err != nil {
		return nil, nil, err
	}

	for _, lvg := range lvgs.Items {
		log.Trace(fmt.Sprintf("[GetStorageClassLVGs] process lvg: %+v", lvg))

		_, ok := storageClassLVGParametersMap[lvg.Name]
		if ok {
			log.Info(fmt.Sprintf("[GetStorageClassLVGs] found lvg from storage class: %s", lvg.Name))
			log.Info(fmt.Sprintf("[GetStorageClassLVGs] lvg.Status.Nodes[0].Name: %s", lvg.Status.Nodes[0].Name))
			storageClassLVGs = append(storageClassLVGs, lvg)
		} else {
			log.Trace(fmt.Sprintf("[GetStorageClassLVGs] skip lvg: %s", lvg.Name))
		}
	}

	return storageClassLVGs, storageClassLVGParametersMap, nil
}

func GetLVGList(ctx context.Context, kc client.Client) (*snc.LVMVolumeGroupList, error) {
	listLvgs := &snc.LVMVolumeGroupList{}
	return listLvgs, kc.List(ctx, listLvgs)
}

func GetLLVSpec(log *logger.Logger, lvName string, selectedLVG snc.LVMVolumeGroup, storageClassLVGParametersMap map[string]string, lvmType string, llvSize resource.Quantity, contiguous bool) snc.LVMLogicalVolumeSpec {
	lvmLogicalVolumeSpec := snc.LVMLogicalVolumeSpec{
		ActualLVNameOnTheNode: lvName,
		Type:                  lvmType,
		Size:                  llvSize.String(),
		LVMVolumeGroupName:    selectedLVG.Name,
	}

	switch lvmType {
	case internal.LVMTypeThin:
		lvmLogicalVolumeSpec.Thin = &snc.LVMLogicalVolumeThinSpec{
			PoolName: storageClassLVGParametersMap[selectedLVG.Name],
		}
		log.Info(fmt.Sprintf("[GetLLVSpec] Thin pool name: %s", lvmLogicalVolumeSpec.Thin.PoolName))
	case internal.LVMTypeThick:
		if contiguous {
			lvmLogicalVolumeSpec.Thick = &snc.LVMLogicalVolumeThickSpec{
				Contiguous: &contiguous,
			}
		}

		log.Info(fmt.Sprintf("[GetLLVSpec] Thick contiguous: %t", contiguous))
	}

	return lvmLogicalVolumeSpec
}

func SelectLVG(storageClassLVGs []snc.LVMVolumeGroup, nodeName string) (snc.LVMVolumeGroup, error) {
	for _, lvg := range storageClassLVGs {
		if lvg.Status.Nodes[0].Name == nodeName {
			return lvg, nil
		}
	}
	return snc.LVMVolumeGroup{}, fmt.Errorf("[SelectLVG] no LVMVolumeGroup found for node %s", nodeName)
}

func removeLLVFinalizerIfExist(ctx context.Context, kc client.Client, log *logger.Logger, llv *snc.LVMLogicalVolume, finalizer string) (bool, error) {
	for attempt := 0; attempt < KubernetesAPIRequestLimit; attempt++ {
		removed := false
		for i, val := range llv.Finalizers {
			if val == finalizer {
				llv.Finalizers = slices.Delete(llv.Finalizers, i, i+1)
				removed = true
				break
			}
		}

		if !removed {
			return false, nil
		}

		log.Trace(fmt.Sprintf("[removeLLVFinalizerIfExist] removing finalizer %s from LVMLogicalVolume %s", finalizer, llv.Name))
		err := kc.Update(ctx, llv)
		if err == nil {
			return true, nil
		}

		if !kerrors.IsConflict(err) {
			return false, fmt.Errorf("[removeLLVFinalizerIfExist] error updating LVMLogicalVolume %s: %w", llv.Name, err)
		}

		if attempt < KubernetesAPIRequestLimit-1 {
			log.Trace(fmt.Sprintf("[removeLLVFinalizerIfExist] conflict while updating LVMLogicalVolume %s, retrying...", llv.Name))
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
				time.Sleep(KubernetesAPIRequestTimeout * time.Second)
				freshLLV, getErr := GetLVMLogicalVolume(ctx, kc, llv.Name, "")
				if getErr != nil {
					return false, fmt.Errorf("[removeLLVFinalizerIfExist] error getting LVMLogicalVolume %s after update conflict: %w", llv.Name, getErr)
				}
				// Update the llv struct with fresh data (without changing pointers because we need the new resource version outside of this function)
				*llv = *freshLLV
			}
		}
	}

	return false, fmt.Errorf("after %d attempts of removing finalizer %s from LVMLogicalVolume %s, last error: %w", KubernetesAPIRequestLimit, finalizer, llv.Name, nil)
}

func IsContiguous(request *csi.CreateVolumeRequest, lvmType string) bool {
	if lvmType == internal.LVMTypeThin {
		return false
	}

	val, exist := request.Parameters[internal.LVMVThickContiguousParamKey]
	if exist {
		return val == "true"
	}

	return false
}