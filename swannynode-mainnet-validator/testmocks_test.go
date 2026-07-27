package main

import (
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// recordingMocks captures every resource's inputs by resource name so tests
// can assert on them after the program runs.
type recordingMocks struct {
	inputs *sync.Map // map[string]resource.PropertyMap
}

func newMocks() *recordingMocks { return &recordingMocks{inputs: &sync.Map{}} }

func (m *recordingMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.inputs.Store(args.Name, args.Inputs)
	return args.Name + "_id", args.Inputs, nil
}

func (m *recordingMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	out := resource.PropertyMap{}
	switch args.Token {
	case "aws:ec2/getVpc:getVpc":
		out["id"] = resource.NewStringProperty("vpc-mock")
	case "aws:ec2/getSubnet:getSubnet":
		out["id"] = resource.NewStringProperty("subnet-mock")
	case "aws:ssm/getParameter:getParameter":
		out["value"] = resource.NewStringProperty("ami-mock")
	}
	return out, nil
}

// get returns the recorded inputs for a resource, or nil.
func (m *recordingMocks) get(name string) resource.PropertyMap {
	if v, ok := m.inputs.Load(name); ok {
		return v.(resource.PropertyMap)
	}
	return nil
}

func testCfg() StackConfig {
	return StackConfig{
		Az: "us-east-2a", InstanceType: "i8g.xlarge",
		UseInstanceStore: true,
		VolumeSizeGb:     600, VolumeIops: 6000, VolumeThroughput: 156,
		ValidatorVolumeSizeGb: 20,
		CreateSecrets:         true,
		RethVersion:           "v2.4.1", LighthouseVersion: "v8.2.0", MevboostVersion: "1.12",
		FeeRecipient: "0x0000000000000000000000000000000000000001",
		KeyName:      "test-key", SshUser: "ec2-user",
	}
}

// ebsCfg is the pre-migration shape: chain data on EBS, no instance store.
func ebsCfg() StackConfig {
	c := testCfg()
	c.InstanceType = "r8g.xlarge"
	c.UseInstanceStore = false
	return c
}
