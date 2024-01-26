/*
Copyright 2023 Flant JSC

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

package driver

import (
	"context"
	"fmt"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"local-lvm-csi/pkg/utils"
	"time"
)

func (d *Driver) NodeStageVolume(ctx context.Context, request *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	d.log.Info("method NodeStageVolume")
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(ctx context.Context, request *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	d.log.Info("method NodeUnstageVolume")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, request *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	d.log.Info("method NodePublishVolume")

	fmt.Println("------------- NodePublishVolume --------------")
	fmt.Println(request)
	fmt.Println("------------- NodePublishVolume --------------")

	vgName := make(map[string]string)
	err := yaml.Unmarshal([]byte(request.GetVolumeContext()[lvmSelector]), &vgName)
	if err != nil {
		d.log.Error(err, "unmarshal labels")
		return nil, status.Error(codes.Internal, "Unmarshal volume context")
	}

	// dev - blockDev /dev/vg-w1/pvc-****
	dev := fmt.Sprintf("/dev/%s/%s", request.GetVolumeContext()[VGNameKey], request.VolumeId)
	// fsType - ext4
	fsType := request.VolumeCapability.GetMount().FsType

	d.log.Info("vgName[VGNameKey] = ", request.GetVolumeContext()[VGNameKey])
	d.log.Info(fmt.Sprintf("[mount] params dev=%s target=%s fs=%s", dev, request.GetTargetPath(), fsType))

	///------------- External code ----------------
	fmt.Println("///------------- External code ----------------")
	fmt.Println("LV Create START")
	deviceSize, err := resource.ParseQuantity("1000000000")
	if err != nil {
		fmt.Println(err)
	}

	lv, err := utils.CreateLV(deviceSize.String(), request.VolumeId, request.GetVolumeContext()[VGNameKey])
	if err != nil {
		d.log.Error(err, "")
	}
	d.log.Info(fmt.Sprintf("[lv create] size=%s pvc=%s vg=%s", deviceSize.String(), request.VolumeId, request.GetVolumeContext()[VGNameKey]))
	fmt.Println("lv create command = ", lv)
	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("LV Create STOP")
	fmt.Println("///------------- External code ----------------")
	time.Sleep(1 * time.Second)
	///------------- External code ----------------

	var mountOptions []string
	if request.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	err = d.mounter.Mount(dev, request.GetTargetPath(), fsType, false, mountOptions)
	if err != nil {
		d.log.Error(err, " d.mounter.Mount ")
		return nil, err
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, request *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	d.log.Info("method NodeUnpublishVolume")
	fmt.Println("------------- NodeUnpublishVolume --------------")
	fmt.Println(request)
	fmt.Println("------------- NodeUnpublishVolume --------------")

	err := d.mounter.Unmount(request.GetTargetPath())
	if err != nil {
		return nil, err
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *Driver) NodeGetVolumeStats(ctx context.Context, request *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	d.log.Info("method NodeGetVolumeStats")
	return &csi.NodeGetVolumeStatsResponse{}, nil
}

func (d *Driver) NodeExpandVolume(ctx context.Context, request *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	d.log.Info("method NodeExpandVolume")
	return &csi.NodeExpandVolumeResponse{}, nil
}

func (d *Driver) NodeGetCapabilities(ctx context.Context, request *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	d.log.Info("method NodeGetCapabilities")
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodeGetInfo(ctx context.Context, request *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	d.log.Info("method NodeGetInfo 0 2")
	d.log.Info("hostID = ", d.hostID)

	return &csi.NodeGetInfoResponse{
		NodeId:            d.hostID,
		MaxVolumesPerNode: 10,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				topologyKey: d.hostID,
			},
		},
	}, nil
}
