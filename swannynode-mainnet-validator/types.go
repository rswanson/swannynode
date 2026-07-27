package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

// StackConfig holds resolved stack configuration as plain values so every
// component is unit-testable without Pulumi config plumbing.
type StackConfig struct {
	Az           string
	InstanceType string

	// UseInstanceStore puts chain data (/data) on the instance's ephemeral
	// NVMe rather than EBS. Chain data is reproducible — reth re-downloads a
	// snapshot and lighthouse checkpoint-syncs — so the durability cost is
	// bounded, while local NVMe cuts page-fault latency roughly 10x. Validator
	// key material NEVER lives here; see ValidatorVolumeSizeGb.
	UseInstanceStore bool

	// VolumeSizeGb / VolumeIops / VolumeThroughput describe the EBS chain-data
	// volume. All three are ignored when UseInstanceStore is true. The IOPS and
	// throughput defaults come from #80 and target the r8g.xlarge EBS baseline
	// ceiling, which is the instance type the EBS path implies.
	VolumeSizeGb     int
	VolumeIops       int
	VolumeThroughput int

	// ValidatorVolumeSizeGb sizes the small EBS volume holding the
	// slashing-protection database and keystores — the only irreplaceable state
	// on the host. Only provisioned when UseInstanceStore is true; on EBS-backed
	// stacks /data is already durable, so the split would be pure churn.
	ValidatorVolumeSizeGb int

	// CreateSecrets controls whether this stack CREATES the Secrets Manager
	// entries or merely reads pre-existing ones. Secret names are fixed and
	// account-unique, so a second stack running in parallel during a migration
	// must set this false or it will collide with the live stack.
	CreateSecrets bool

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

// getBoolOr distinguishes "unset" from "explicitly false", which cfg.GetBool
// alone cannot do — an unset key and `false` both read as false.
func getBoolOr(cfg *config.Config, key string, def bool) bool {
	if cfg.Get(key) != "" {
		return cfg.GetBool(key)
	}
	return def
}

// validatorDataDir is where keystores and the slashing-protection DB live.
//
// It follows the storage mode rather than being fixed. On instance-store stacks
// /data is ephemeral, so validator state must sit on its own EBS volume. On
// EBS-backed stacks /data is already durable and the historical location is
// correct — moving it would repoint a live validator at an empty datadir, and
// validator-init would then re-import a stale interchange from Secrets Manager.
func (c StackConfig) validatorDataDir() string {
	if c.UseInstanceStore {
		return "/validator/lighthouse"
	}
	return "/data/mainnet/lighthouse"
}

func loadStackConfig(cfg *config.Config) StackConfig {
	return StackConfig{
		Az:                    getOr(cfg, "az", "us-east-2a"),
		InstanceType:          getOr(cfg, "instanceType", "i8g.xlarge"),
		UseInstanceStore:      getBoolOr(cfg, "useInstanceStore", true),
		VolumeSizeGb:          getIntOr(cfg, "volumeSizeGb", 600),
		VolumeIops:            getIntOr(cfg, "volumeIops", 6000),
		VolumeThroughput:      getIntOr(cfg, "volumeThroughput", 156),
		ValidatorVolumeSizeGb: getIntOr(cfg, "validatorVolumeSizeGb", 20),
		CreateSecrets:         getBoolOr(cfg, "createSecrets", true),
		RethVersion:           getOr(cfg, "rethVersion", "v2.4.1"),
		LighthouseVersion:     getOr(cfg, "lighthouseVersion", "v8.2.0"),
		MevboostVersion:       getOr(cfg, "mevboostVersion", "1.12"),
		FeeRecipient:          cfg.Require("feeRecipient"),
		KeyName:               cfg.Require("keyName"),
		SshUser:               getOr(cfg, "sshUser", "ubuntu"),
	}
}
