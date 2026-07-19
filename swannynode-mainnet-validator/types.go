package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// StackConfig holds resolved stack configuration as plain values so every
// component is unit-testable without Pulumi config plumbing.
type StackConfig struct {
	Az                string
	InstanceType      string
	VolumeSizeGb      int
	VolumeIops        int
	RethVersion       string
	LighthouseVersion string
	MevboostVersion   string
	FeeRecipient      string
	KeyName           string
	SshUser           string
}

func getOr(cfg *config.Config, key, def string) string {
	if v := cfg.Get(key); v != "" {
		return v
	}
	return def
}

func getIntOr(cfg *config.Config, key string, def int) int {
	if v := cfg.GetInt(key); v != 0 {
		return v
	}
	return def
}

func loadStackConfig(cfg *config.Config) StackConfig {
	return StackConfig{
		Az:                getOr(cfg, "az", "us-east-2a"),
		InstanceType:      getOr(cfg, "instanceType", "r8g.xlarge"),
		VolumeSizeGb:      getIntOr(cfg, "volumeSizeGb", 600),
		VolumeIops:        getIntOr(cfg, "volumeIops", 3000),
		RethVersion:       getOr(cfg, "rethVersion", "v2.4.1"),
		LighthouseVersion: getOr(cfg, "lighthouseVersion", "v8.2.0"),
		MevboostVersion:   getOr(cfg, "mevboostVersion", "1.12"),
		FeeRecipient:      cfg.Require("feeRecipient"),
		KeyName:           cfg.Require("keyName"),
		SshUser:           getOr(cfg, "sshUser", "ec2-user"),
	}
}
