package virt

import (
	"slices"
	"testing"
)

const vmwareOVF = `<?xml version="1.0" encoding="UTF-8"?>
<Envelope vmw:buildId="build-1" xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1" xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData" xmlns:vmw="http://www.vmware.com/schema/ovf">
  <References>
    <File ovf:href="web-disk1.vmdk" ovf:id="file1" ovf:size="1000"/>
    <File ovf:href="web-disk2.vmdk" ovf:id="file2" ovf:size="1000"/>
  </References>
  <DiskSection>
    <Disk ovf:capacity="20" ovf:capacityAllocationUnits="byte * 2^30" ovf:diskId="vmdisk1" ovf:fileRef="file1"/>
    <Disk ovf:capacity="5" ovf:capacityAllocationUnits="byte * 2^30" ovf:diskId="vmdisk2" ovf:fileRef="file2"/>
  </DiskSection>
  <VirtualSystem ovf:id="web01">
    <Name>web01</Name>
    <OperatingSystemSection ovf:id="1" vmw:osType="windows2019srv_64Guest"><Description>Microsoft Windows Server 2019</Description></OperatingSystemSection>
    <VirtualHardwareSection>
      <Item><rasd:AllocationUnits>hertz * 10^6</rasd:AllocationUnits><rasd:ResourceType>3</rasd:ResourceType><rasd:VirtualQuantity>4</rasd:VirtualQuantity></Item>
      <Item><rasd:AllocationUnits>byte * 2^30</rasd:AllocationUnits><rasd:ResourceType>4</rasd:ResourceType><rasd:VirtualQuantity>8</rasd:VirtualQuantity></Item>
      <Item><rasd:HostResource>ovf:/disk/vmdisk1</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
      <Item><rasd:HostResource>ovf:/disk/vmdisk2</rasd:HostResource><rasd:ResourceType>17</rasd:ResourceType></Item>
      <vmw:Config ovf:required="false" vmw:key="firmware" vmw:value="efi"/>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`

func TestParseOVF(t *testing.T) {
	o, err := ParseOVF([]byte(vmwareOVF))
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "web01" || o.VCPUs != 4 || o.MemoryMiB != 8192 || !o.UEFI || !o.Windows {
		t.Errorf("parsed %+v", o)
	}
	if !slices.Equal(o.Disks, []string{"web-disk1.vmdk", "web-disk2.vmdk"}) {
		t.Errorf("disks %v", o.Disks)
	}
	for units, want := range map[string]uint64{"byte * 2^20": 512, "MegaBytes": 512, "byte * 2^30": 512 * 1024, "": 512} {
		if got := toMiB(512, units); got != want {
			t.Errorf("toMiB(512, %q) = %d, want %d", units, got, want)
		}
	}
	if _, err := ParseOVF([]byte("not xml")); err == nil {
		t.Error("garbage accepted")
	}
}
