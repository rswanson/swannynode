# Implementation Plan: swannynode-mainnet-validator

**Spec:** `docs/specs/2026-07-19-mainnet-validator-design.md`
**Worktree:** `~/git/swannynode-wt/mainnet-validator`, branch `swanny/mainnet-validator`
**Date:** 2026-07-19

New Pulumi Go project `swannynode-mainnet-validator/` that provisions and configures a cost-optimized mainnet reth `--minimal` validator: r8g.xlarge Graviton instance, persistent 600GB gp3 EBS data volume (protected), Secrets Manager for keys, DLM snapshots, and SSH-based client bootstrap with an idempotent reth snapshot-init step.

Pinned versions: reth `v2.4.1`, lighthouse `v8.2.0`, mev-boost `1.12` (all have `aarch64`/`arm64` Linux release assets).

## File structure

```
swannynode-mainnet-validator/
├── Pulumi.yaml                  — project metadata (runtime go)
├── go.mod / go.sum              — deps: pulumi sdk v3, pulumi-aws v6, pulumi-command v1, testify
├── main.go                      — reads stack config into StackConfig; wires components
├── types.go                     — StackConfig struct + config helpers
├── network.go                   — default VPC/subnet lookup, security group, Elastic IP
├── storage.go                   — EBS data volume (protected) + DLM snapshot policy + DLM role
├── identity.go                  — 3 Secrets Manager secrets, IAM role/policy/instance profile
├── compute.go                   — AL2023 arm64 AMI lookup, EC2 instance, volume attachment, EIP assoc
├── deploy.go                    — remote.Command bootstrap over SSH + file copies + systemd enable
├── testmocks_test.go            — shared Pulumi mocks + testCfg()
├── network_test.go / storage_test.go / identity_test.go / compute_test.go / deploy_test.go
├── config/                      — systemd units
│   ├── reth-init.service        — oneshot snapshot download (guarded)
│   ├── reth.service             — reth node --minimal
│   ├── lighthousebeacon.service — lighthouse bn
│   ├── validator-init.service   — oneshot: pull secrets, import keystore + slashing DB
│   ├── lighthousevalidator.service — lighthouse vc
│   └── mevboost.service
├── scripts/                     — installed to /data/scripts on the host
│   ├── mount_data.sh            — format-if-blank + fstab + mount /data
│   ├── install_clients.sh       — download pinned client binaries to /data/bin
│   ├── reth_init.sh             — datadir-empty guard + reth download --minimal (retry loop)
│   ├── fetch_and_import_validator.sh — secrets → keystore import → slashing import
│   ├── start_reth.sh / start_lighthouse_bn.sh / start_lighthouse_vc.sh / start_mevboost.sh
├── scripts_test/                — local shell tests (run on macOS, no AWS needed)
│   ├── test_mount_data.sh
│   ├── test_reth_init.sh
│   ├── test_import.sh
│   └── test_units.sh
└── README.md                    — config reference + migration & recovery runbook
```

Also modified: `go.work` (add `./swannynode-mainnet-validator`).

## Execution plan (waves)

| Wave | Tasks | Notes |
|---|---|---|
| 1 | T1 | scaffold + shared test mocks |
| 2 | T2 storage, T3 network, T4 identity, T5 mount/init scripts, T6 validator/start scripts, T7 systemd units | all independent; depend only on T1 |
| 3 | T8 compute + main.go infra wiring | needs T2–T4 types |
| 4 | T9 deploy.go + main.go final wiring | needs T5–T8 |
| 5 | T10 stack init, preview, README runbook | needs everything |

No two tasks in a wave touch the same file. `main.go` is touched by T1 (wave 1), T8 (wave 3), T9 (wave 4) — different waves.

Conventions for all tasks: work in the worktree (`cd ~/git/swannynode-wt/mainnet-validator`); verify branch with `git branch --show-current` → `swanny/mainnet-validator`. Go commands run from `swannynode-mainnet-validator/`. Shell tests run with plain `bash`.

---

## Task T1 — Project scaffold, config types, shared test mocks

**Files:** create `swannynode-mainnet-validator/{Pulumi.yaml,go.mod,main.go,types.go,testmocks_test.go}`; modify `go.work`
**Depends-on:** — 
**Touches:** `swannynode-mainnet-validator/Pulumi.yaml`, `swannynode-mainnet-validator/go.mod`, `swannynode-mainnet-validator/main.go`, `swannynode-mainnet-validator/types.go`, `swannynode-mainnet-validator/testmocks_test.go`, `go.work`

**Step 1 — failing check.** Run:
```sh
cd ~/git/swannynode-wt/mainnet-validator/swannynode-mainnet-validator && go build ./...
```
Expected: fails — directory does not exist.

**Step 2 — scaffold.** Create `swannynode-mainnet-validator/Pulumi.yaml`:
```yaml
name: swannynode-mainnet-validator
runtime: go
description: Cost-optimized mainnet reth --minimal validator (EC2 + EBS + Secrets Manager)
```

Create `swannynode-mainnet-validator/types.go`:
```go
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
```

Create `swannynode-mainnet-validator/main.go` (stub; wiring lands in T8/T9):
```go
package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		sc := loadStackConfig(cfg)
		_ = sc // wired in later tasks
		return nil
	})
}
```

Create `swannynode-mainnet-validator/testmocks_test.go`:
```go
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
		Az: "us-east-2a", InstanceType: "r8g.xlarge",
		VolumeSizeGb: 600, VolumeIops: 3000,
		RethVersion: "v2.4.1", LighthouseVersion: "v8.2.0", MevboostVersion: "1.12",
		FeeRecipient: "0x0000000000000000000000000000000000000001",
		KeyName:      "test-key", SshUser: "ec2-user",
	}
}
```

Initialize the module and deps:
```sh
cd ~/git/swannynode-wt/mainnet-validator/swannynode-mainnet-validator
go mod init swannynode-mainnet-validator
go get github.com/pulumi/pulumi/sdk/v3
go get github.com/pulumi/pulumi-aws/sdk/v6
go get github.com/pulumi/pulumi-command/sdk@latest
go get github.com/stretchr/testify
```

Add the project to `go.work` — edit the `use (...)` block to include (alphabetical position after `./swannynode-holesky`):
```
	./swannynode-mainnet-validator
```

**Step 3 — run to pass.**
```sh
go build ./... && go vet ./... && go test ./...
```
Expected: build OK; `go test` reports `ok` (mocks compile; no test funcs yet is fine — testmocks_test.go has no Test functions, output `ok ... [no test files]` or `ok`).

**Step 4 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): scaffold Pulumi Go project with config types and test mocks"
```

---

## Task T2 — storage.go: protected EBS data volume + DLM daily snapshots

**Files:** create `swannynode-mainnet-validator/{storage.go,storage_test.go}`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/storage.go`, `swannynode-mainnet-validator/storage_test.go`

**Step 1 — failing test.** Create `storage_test.go`:
```go
package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestStorageVolumeShape(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	vol := m.get("validator-data")
	require.NotNil(t, vol, "expected EBS volume resource 'validator-data'")
	require.Equal(t, 600.0, vol["size"].NumberValue())
	require.Equal(t, "gp3", vol["type"].StringValue())
	require.Equal(t, "us-east-2a", vol["availabilityZone"].StringValue())
	tags := vol["tags"].ObjectValue()
	require.Equal(t, "swannynode-mainnet-validator", tags["Backup"].StringValue())
}

func TestStorageDlmPolicy(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createStorage(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	pol := m.get("validator-data-snapshots")
	require.NotNil(t, pol, "expected DLM lifecycle policy 'validator-data-snapshots'")
	details := pol["policyDetails"].ObjectValue()
	sched := details["schedules"].ArrayValue()[0].ObjectValue()
	require.Equal(t, 7.0, sched["retainRule"].ObjectValue()["count"].NumberValue())
	require.Equal(t, 24.0, sched["createRule"].ObjectValue()["interval"].NumberValue())
}
```

**Step 2 — run to fail.**
```sh
go test ./... -run TestStorage
```
Expected: compile error — `undefined: createStorage`.

**Step 3 — implement.** Create `storage.go`:
```go
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/dlm"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ebs"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const backupTag = "swannynode-mainnet-validator"

type Storage struct {
	Volume *ebs.Volume
}

// createStorage provisions the persistent chain-data volume and its snapshot
// policy. The volume is Protect()ed and RetainOnDelete so no Pulumi operation
// can destroy chain data.
func createStorage(ctx *pulumi.Context, cfg StackConfig) (*Storage, error) {
	vol, err := ebs.NewVolume(ctx, "validator-data", &ebs.VolumeArgs{
		AvailabilityZone: pulumi.String(cfg.Az),
		Size:             pulumi.Int(cfg.VolumeSizeGb),
		Type:             pulumi.String("gp3"),
		Iops:             pulumi.Int(cfg.VolumeIops),
		Tags: pulumi.StringMap{
			"Name":   pulumi.String("swannynode-mainnet-validator-data"),
			"Backup": pulumi.String(backupTag),
		},
	}, pulumi.Protect(true), pulumi.RetainOnDelete(true))
	if err != nil {
		return nil, err
	}

	dlmRole, err := iam.NewRole(ctx, "dlm-lifecycle-role", &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Principal": {"Service": "dlm.amazonaws.com"},
				"Action": "sts:AssumeRole"
			}]
		}`),
	})
	if err != nil {
		return nil, err
	}
	_, err = iam.NewRolePolicyAttachment(ctx, "dlm-lifecycle-attach", &iam.RolePolicyAttachmentArgs{
		Role:      dlmRole.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/service-role/AWSDataLifecycleManagerServiceRole"),
	})
	if err != nil {
		return nil, err
	}

	_, err = dlm.NewLifecyclePolicy(ctx, "validator-data-snapshots", &dlm.LifecyclePolicyArgs{
		Description:      pulumi.String("Daily snapshots of the mainnet validator data volume"),
		ExecutionRoleArn: dlmRole.Arn,
		State:            pulumi.String("ENABLED"),
		PolicyDetails: &dlm.LifecyclePolicyPolicyDetailsArgs{
			ResourceTypes: pulumi.StringArray{pulumi.String("VOLUME")},
			TargetTags:    pulumi.StringMap{"Backup": pulumi.String(backupTag)},
			Schedules: dlm.LifecyclePolicyPolicyDetailsScheduleArray{
				&dlm.LifecyclePolicyPolicyDetailsScheduleArgs{
					Name: pulumi.String("daily"),
					CreateRule: &dlm.LifecyclePolicyPolicyDetailsScheduleCreateRuleArgs{
						Interval:     pulumi.Int(24),
						IntervalUnit: pulumi.String("HOURS"),
						Times:        pulumi.String("09:00"),
					},
					RetainRule: &dlm.LifecyclePolicyPolicyDetailsScheduleRetainRuleArgs{
						Count: pulumi.Int(7),
					},
					CopyTags: pulumi.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return &Storage{Volume: vol}, nil
}
```
Note: if `go build` reports a field type mismatch on `Times` (some pulumi-aws v6 versions type it as `pulumi.StringArrayInput`), use `Times: pulumi.StringArray{pulumi.String("09:00")}` — check the installed SDK's signature and match it; the test does not assert on `times`.

**Step 4 — run to pass.**
```sh
go test ./... -run TestStorage
```
Expected: `ok  swannynode-mainnet-validator`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): protected EBS data volume with daily DLM snapshots"
```

---

## Task T3 — network.go: default VPC lookup, security group, Elastic IP

**Files:** create `swannynode-mainnet-validator/{network.go,network_test.go}`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/network.go`, `swannynode-mainnet-validator/network_test.go`

**Step 1 — failing test.** Create `network_test.go`:
```go
package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func ingressPorts(sg resource.PropertyMap) map[float64][]string {
	got := map[float64][]string{}
	for _, r := range sg["ingress"].ArrayValue() {
		rule := r.ObjectValue()
		port := rule["fromPort"].NumberValue()
		got[port] = append(got[port], rule["protocol"].StringValue())
	}
	return got
}

func TestNetworkSecurityGroupPorts(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createNetwork(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	sg := m.get("validator-sg")
	require.NotNil(t, sg, "expected security group 'validator-sg'")
	ports := ingressPorts(sg)
	require.ElementsMatch(t, []string{"tcp"}, ports[22], "ssh")
	require.ElementsMatch(t, []string{"tcp", "udp"}, ports[30303], "reth p2p")
	require.ElementsMatch(t, []string{"tcp", "udp"}, ports[9000], "lighthouse p2p")
	require.ElementsMatch(t, []string{"udp"}, ports[9001], "lighthouse quic")
	require.Len(t, ports, 4, "no unexpected ingress ports")
}

func TestNetworkEip(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createNetwork(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)
	require.NotNil(t, m.get("validator-eip"), "expected Elastic IP 'validator-eip'")
}
```

**Step 2 — run to fail.**
```sh
go test ./... -run TestNetwork
```
Expected: compile error — `undefined: createNetwork`.

**Step 3 — implement.** Create `network.go`:
```go
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Network struct {
	SecurityGroup *ec2.SecurityGroup
	SubnetId      string
	Eip           *ec2.Eip
}

func anywhere() pulumi.StringArray {
	return pulumi.StringArray{pulumi.String("0.0.0.0/0")}
}

func ingress(proto string, port int) *ec2.SecurityGroupIngressArgs {
	return &ec2.SecurityGroupIngressArgs{
		Protocol:   pulumi.String(proto),
		FromPort:   pulumi.Int(port),
		ToPort:     pulumi.Int(port),
		CidrBlocks: anywhere(),
	}
}

// createNetwork looks up the default VPC subnet in the configured AZ and
// creates the validator security group and Elastic IP. Metrics ports are
// deliberately NOT exposed; only p2p and SSH.
func createNetwork(ctx *pulumi.Context, cfg StackConfig) (*Network, error) {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return nil, err
	}
	subnet, err := ec2.LookupSubnet(ctx, &ec2.LookupSubnetArgs{
		VpcId:            pulumi.StringRef(vpc.Id),
		AvailabilityZone: pulumi.StringRef(cfg.Az),
		DefaultForAz:     pulumi.BoolRef(true),
	})
	if err != nil {
		return nil, err
	}

	sg, err := ec2.NewSecurityGroup(ctx, "validator-sg", &ec2.SecurityGroupArgs{
		VpcId:       pulumi.String(vpc.Id),
		Description: pulumi.String("mainnet validator: p2p + ssh only"),
		Ingress: ec2.SecurityGroupIngressArray{
			ingress("tcp", 22),
			ingress("tcp", 30303), // reth p2p
			ingress("udp", 30303),
			ingress("tcp", 9000), // lighthouse p2p
			ingress("udp", 9000),
			ingress("udp", 9001), // lighthouse quic
		},
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				Protocol:   pulumi.String("-1"),
				FromPort:   pulumi.Int(0),
				ToPort:     pulumi.Int(0),
				CidrBlocks: anywhere(),
			},
		},
		Tags: pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	})
	if err != nil {
		return nil, err
	}

	eip, err := ec2.NewEip(ctx, "validator-eip", &ec2.EipArgs{
		Domain: pulumi.String("vpc"),
		Tags:   pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	})
	if err != nil {
		return nil, err
	}

	return &Network{SecurityGroup: sg, SubnetId: subnet.Id, Eip: eip}, nil
}
```

**Step 4 — run to pass.**
```sh
go test ./... -run TestNetwork
```
Expected: `ok`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): security group, default-VPC subnet lookup, Elastic IP"
```

---

## Task T4 — identity.go: Secrets Manager secrets + instance IAM role

**Files:** create `swannynode-mainnet-validator/{identity.go,identity_test.go}`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/identity.go`, `swannynode-mainnet-validator/identity_test.go`

**Step 1 — failing test.** Create `identity_test.go`:
```go
package main

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestIdentitySecretsCreated(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createIdentity(ctx)
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	for _, n := range []string{"keystore", "keystore-password", "slashing-protection"} {
		sec := m.get("validator-secret-" + n)
		require.NotNil(t, sec, "expected secret validator-secret-"+n)
		require.Equal(t, "mainnet-validator/"+n, sec["name"].StringValue())
	}
}

func TestIdentityRolePolicyReadsSecretsOnly(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createIdentity(ctx)
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	rp := m.get("validator-secrets-read")
	require.NotNil(t, rp, "expected inline role policy 'validator-secrets-read'")
	policy := rp["policy"].StringValue()
	require.True(t, strings.Contains(policy, "secretsmanager:GetSecretValue"))
	require.False(t, strings.Contains(policy, "\"*\""), "policy must not grant on all resources")
}
```

**Step 2 — run to fail.**
```sh
go test ./... -run TestIdentity
```
Expected: compile error — `undefined: createIdentity`.

**Step 3 — implement.** Create `identity.go`:
```go
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const secretPrefix = "mainnet-validator"

type Identity struct {
	Profile *iam.InstanceProfile
	Secrets map[string]*secretsmanager.Secret
}

// createIdentity provisions the three key-material secrets (created EMPTY —
// values are pushed out-of-band so they never enter Pulumi state) and an
// instance role that can read exactly those secrets plus use SSM.
func createIdentity(ctx *pulumi.Context) (*Identity, error) {
	names := []string{"keystore", "keystore-password", "slashing-protection"}
	secrets := map[string]*secretsmanager.Secret{}
	arns := []interface{}{}
	for _, n := range names {
		s, err := secretsmanager.NewSecret(ctx, "validator-secret-"+n, &secretsmanager.SecretArgs{
			Name:        pulumi.String(secretPrefix + "/" + n),
			Description: pulumi.String("mainnet validator " + n + " (value pushed out-of-band)"),
		})
		if err != nil {
			return nil, err
		}
		secrets[n] = s
		arns = append(arns, s.Arn)
	}

	role, err := iam.NewRole(ctx, "validator-instance-role", &iam.RoleArgs{
		AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Principal": {"Service": "ec2.amazonaws.com"},
				"Action": "sts:AssumeRole"
			}]
		}`),
	})
	if err != nil {
		return nil, err
	}

	policyJSON := pulumi.All(arns...).ApplyT(func(vs []interface{}) (string, error) {
		return `{
			"Version": "2012-10-17",
			"Statement": [{
				"Effect": "Allow",
				"Action": "secretsmanager:GetSecretValue",
				"Resource": ["` + vs[0].(string) + `", "` + vs[1].(string) + `", "` + vs[2].(string) + `"]
			}]
		}`, nil
	}).(pulumi.StringOutput)

	_, err = iam.NewRolePolicy(ctx, "validator-secrets-read", &iam.RolePolicyArgs{
		Role:   role.ID(),
		Policy: policyJSON,
	})
	if err != nil {
		return nil, err
	}

	_, err = iam.NewRolePolicyAttachment(ctx, "validator-ssm-core", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"),
	})
	if err != nil {
		return nil, err
	}

	profile, err := iam.NewInstanceProfile(ctx, "validator-instance-profile", &iam.InstanceProfileArgs{
		Role: role.Name,
	})
	if err != nil {
		return nil, err
	}

	return &Identity{Profile: profile, Secrets: secrets}, nil
}
```

**Step 4 — run to pass.**
```sh
go test ./... -run TestIdentity
```
Expected: `ok`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): Secrets Manager secrets and least-privilege instance role"
```

---

## Task T5 — mount + reth-init scripts with local shell tests

**Files:** create `swannynode-mainnet-validator/scripts/{mount_data.sh,reth_init.sh,install_clients.sh}`, `swannynode-mainnet-validator/scripts_test/{test_mount_data.sh,test_reth_init.sh}`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/scripts/mount_data.sh`, `swannynode-mainnet-validator/scripts/reth_init.sh`, `swannynode-mainnet-validator/scripts/install_clients.sh`, `swannynode-mainnet-validator/scripts_test/test_mount_data.sh`, `swannynode-mainnet-validator/scripts_test/test_reth_init.sh`

**Step 1 — failing tests.** Create `scripts_test/test_reth_init.sh`:
```bash
#!/usr/bin/env bash
# Tests reth_init.sh: downloads only when the datadir is empty.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

# Stub reth binary that records its invocation.
cat > "$tmp/reth" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "$(dirname "$0")/reth_calls.log"
exit 0
EOF
chmod +x "$tmp/reth"

# Case 1: empty datadir -> download runs with --minimal.
DATADIR="$tmp/empty" RETH_BIN="$tmp/reth" RETRY_DELAY=0 bash scripts/reth_init.sh
grep -q -- "download --minimal" "$tmp/reth_calls.log" || { echo "FAIL: expected download --minimal for empty datadir"; exit 1; }

# Case 2: initialized datadir -> no download.
rm -f "$tmp/reth_calls.log"
mkdir -p "$tmp/full/db" && touch "$tmp/full/db/mdbx.dat"
DATADIR="$tmp/full" RETH_BIN="$tmp/reth" RETRY_DELAY=0 bash scripts/reth_init.sh
[ ! -f "$tmp/reth_calls.log" ] || { echo "FAIL: download must be skipped when db exists"; exit 1; }

# Case 3: failing download retries then errors.
cat > "$tmp/reth" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$tmp/reth"
if DATADIR="$tmp/empty2" RETH_BIN="$tmp/reth" RETRY_DELAY=0 MAX_ATTEMPTS=2 bash scripts/reth_init.sh 2>/dev/null; then
  echo "FAIL: expected non-zero exit after exhausted retries"; exit 1
fi

echo "PASS test_reth_init"
```

Create `scripts_test/test_mount_data.sh`:
```bash
#!/usr/bin/env bash
# Tests mount_data.sh: formats only blank devices, appends fstab once.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
touch "$tmp/dev-node" "$tmp/fstab"
echo -n > "$tmp/calls.log"

for tool in blkid mkfs.ext4 mount mountpoint; do
  cat > "$tmp/bin/$tool" <<EOF
#!/usr/bin/env bash
echo "$tool \$@" >> "$tmp/calls.log"
EOF
  chmod +x "$tmp/bin/$tool"
done
# blkid behavior: fail (blank device) unless BLKID_HAS_FS marker exists; -s UUID prints a fixed UUID.
cat > "$tmp/bin/blkid" <<EOF
#!/usr/bin/env bash
echo "blkid \$@" >> "$tmp/calls.log"
if [ "\$1" = "-s" ]; then echo "test-uuid-1234"; exit 0; fi
[ -f "$tmp/BLKID_HAS_FS" ] || exit 2
EOF
chmod +x "$tmp/bin/blkid"

# Case 1: blank device -> mkfs runs, fstab gains one line.
PATH="$tmp/bin:$PATH" VOLUME_ID=vol-abc123 DEV_OVERRIDE="$tmp/dev-node" FSTAB="$tmp/fstab" MOUNT="$tmp/mnt" bash scripts/mount_data.sh
grep -q "mkfs.ext4" "$tmp/calls.log" || { echo "FAIL: expected mkfs on blank device"; exit 1; }
[ "$(grep -c "test-uuid-1234" "$tmp/fstab")" = "1" ] || { echo "FAIL: expected one fstab entry"; exit 1; }

# Case 2: device already has a filesystem -> no mkfs, fstab unchanged (idempotent re-run).
touch "$tmp/BLKID_HAS_FS"; echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" VOLUME_ID=vol-abc123 DEV_OVERRIDE="$tmp/dev-node" FSTAB="$tmp/fstab" MOUNT="$tmp/mnt" bash scripts/mount_data.sh
grep -q "mkfs.ext4" "$tmp/calls.log" && { echo "FAIL: mkfs must not run on formatted device"; exit 1; }
[ "$(grep -c "test-uuid-1234" "$tmp/fstab")" = "1" ] || { echo "FAIL: fstab entry duplicated"; exit 1; }

echo "PASS test_mount_data"
```

**Step 2 — run to fail.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_reth_init.sh; bash swannynode-mainnet-validator/scripts_test/test_mount_data.sh
```
Expected: both fail — scripts do not exist.

**Step 3 — implement.** Create `scripts/reth_init.sh`:
```bash
#!/usr/bin/env bash
# One-time reth snapshot initialization. Idempotent: a datadir that already
# contains a database is NEVER touched — this is the guard that makes every
# recovery path (fresh volume, reattached volume) safe to converge through.
set -euo pipefail
DATADIR="${DATADIR:-/data/mainnet/reth}"
RETH_BIN="${RETH_BIN:-/data/bin/reth}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_DELAY="${RETRY_DELAY:-30}"

if [ -f "$DATADIR/db/mdbx.dat" ]; then
  echo "reth datadir already initialized; skipping snapshot download"
  exit 0
fi

mkdir -p "$DATADIR"
attempt=0
# reth download supports HTTP-Range resume, so re-invoking after an
# interruption continues the download rather than restarting it.
until "$RETH_BIN" download --minimal -y --chain mainnet --datadir "$DATADIR"; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "snapshot download failed after $MAX_ATTEMPTS attempts" >&2
    exit 1
  fi
  echo "download interrupted; resuming in ${RETRY_DELAY}s (attempt $attempt/$MAX_ATTEMPTS)"
  sleep "$RETRY_DELAY"
done
echo "snapshot download complete"
```

Create `scripts/mount_data.sh`:
```bash
#!/usr/bin/env bash
# Format-if-blank and mount the EBS data volume at /data.
# Never formats a device that already has a filesystem (a reattached volume).
set -euo pipefail
VOLUME_ID="${VOLUME_ID:?VOLUME_ID (vol-...) is required}"
MOUNT="${MOUNT:-/data}"
FSTAB="${FSTAB:-/etc/fstab}"
# On Nitro, EBS volumes surface as NVMe with the volume id (sans dash) as serial.
DEV="${DEV_OVERRIDE:-/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${VOLUME_ID/-/}}"

for _ in $(seq 1 60); do
  [ -e "$DEV" ] && break
  sleep 2
done
[ -e "$DEV" ] || { echo "device $DEV not found after 120s" >&2; exit 1; }

if ! blkid "$DEV" >/dev/null 2>&1; then
  echo "blank device; creating ext4 filesystem"
  mkfs.ext4 -L validator-data "$DEV"
fi

UUID=$(blkid -s UUID -o value "$DEV")
if ! grep -q "UUID=$UUID" "$FSTAB"; then
  echo "UUID=$UUID $MOUNT ext4 defaults,nofail 0 2" >> "$FSTAB"
fi
mkdir -p "$MOUNT"
mount -a
mountpoint -q "$MOUNT" || { echo "$MOUNT is not mounted" >&2; exit 1; }
echo "data volume mounted at $MOUNT"
```

Create `scripts/install_clients.sh` (no dedicated unit test — exercised by T9's bootstrap-content test; download URLs verified in T10 preview task):
```bash
#!/usr/bin/env bash
# Install pinned client release binaries (arm64) into /data/bin.
# Skips a client whose installed version already matches the pin.
set -euo pipefail
RETH_VERSION="${RETH_VERSION:?}"        # e.g. v2.4.1
LIGHTHOUSE_VERSION="${LIGHTHOUSE_VERSION:?}" # e.g. v8.2.0
MEVBOOST_VERSION="${MEVBOOST_VERSION:?}"     # e.g. 1.12
BIN="${BIN:-/data/bin}"
mkdir -p "$BIN"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

if ! "$BIN/reth" --version 2>/dev/null | grep -qF "${RETH_VERSION#v}"; then
  curl -fsSL -o "$tmp/reth.tgz" \
    "https://github.com/paradigmxyz/reth/releases/download/${RETH_VERSION}/reth-${RETH_VERSION}-aarch64-unknown-linux-gnu.tar.gz"
  tar xzf "$tmp/reth.tgz" -C "$tmp" reth
  install -m 0755 "$tmp/reth" "$BIN/reth"
fi

if ! "$BIN/lighthouse" --version 2>/dev/null | grep -qF "${LIGHTHOUSE_VERSION#v}"; then
  curl -fsSL -o "$tmp/lighthouse.tgz" \
    "https://github.com/sigp/lighthouse/releases/download/${LIGHTHOUSE_VERSION}/lighthouse-${LIGHTHOUSE_VERSION}-aarch64-unknown-linux-gnu.tar.gz"
  tar xzf "$tmp/lighthouse.tgz" -C "$tmp" lighthouse
  install -m 0755 "$tmp/lighthouse" "$BIN/lighthouse"
fi

if ! "$BIN/mev-boost" --version 2>/dev/null | grep -qF "${MEVBOOST_VERSION}"; then
  curl -fsSL -o "$tmp/mev-boost.tgz" \
    "https://github.com/flashbots/mev-boost/releases/download/v${MEVBOOST_VERSION}/mev-boost_${MEVBOOST_VERSION}_linux_arm64.tar.gz"
  tar xzf "$tmp/mev-boost.tgz" -C "$tmp" mev-boost
  install -m 0755 "$tmp/mev-boost" "$BIN/mev-boost"
fi
echo "client binaries installed: reth ${RETH_VERSION}, lighthouse ${LIGHTHOUSE_VERSION}, mev-boost ${MEVBOOST_VERSION}"
```

```sh
chmod +x swannynode-mainnet-validator/scripts/*.sh swannynode-mainnet-validator/scripts_test/*.sh
```

**Step 4 — run to pass.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_reth_init.sh && bash swannynode-mainnet-validator/scripts_test/test_mount_data.sh
```
Expected: `PASS test_reth_init` and `PASS test_mount_data`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): mount, snapshot-init, and client-install scripts with guards"
```

---

## Task T6 — validator import + client start scripts

**Files:** create `swannynode-mainnet-validator/scripts/{fetch_and_import_validator.sh,start_reth.sh,start_lighthouse_bn.sh,start_lighthouse_vc.sh,start_mevboost.sh}`, `swannynode-mainnet-validator/scripts_test/test_import.sh`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/scripts/fetch_and_import_validator.sh`, `swannynode-mainnet-validator/scripts/start_reth.sh`, `swannynode-mainnet-validator/scripts/start_lighthouse_bn.sh`, `swannynode-mainnet-validator/scripts/start_lighthouse_vc.sh`, `swannynode-mainnet-validator/scripts/start_mevboost.sh`, `swannynode-mainnet-validator/scripts_test/test_import.sh`

**Step 1 — failing test.** Create `scripts_test/test_import.sh`:
```bash
#!/usr/bin/env bash
# Tests fetch_and_import_validator.sh: pulls secrets, imports keystore and
# slashing data; skips entirely when the validator is already imported;
# fails hard when keystore secret is unavailable.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

cat > "$tmp/bin/aws" <<EOF
#!/usr/bin/env bash
echo "aws \$@" >> "$tmp/calls.log"
# args: secretsmanager get-secret-value --secret-id <id> ...
[ -f "$tmp/SECRETS_FAIL" ] && exit 1
case "\$4" in
  */keystore) echo '{"version":4}';;
  */keystore-password) echo 'hunter2';;
  */slashing-protection) echo '{"metadata":{}}';;
esac
EOF
cat > "$tmp/bin/lighthouse" <<EOF
#!/usr/bin/env bash
echo "lighthouse \$@" >> "$tmp/calls.log"
EOF
cat > "$tmp/bin/chown" <<EOF
#!/usr/bin/env bash
echo "chown \$@" >> "$tmp/calls.log"
EOF
chmod +x "$tmp/bin/"*

# Case 1: fresh datadir -> import + slashing import run.
echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh
grep -q "account validator import" "$tmp/calls.log" || { echo "FAIL: keystore import not invoked"; exit 1; }
grep -q "slashing-protection import" "$tmp/calls.log" || { echo "FAIL: slashing import not invoked"; exit 1; }
grep -q "chown -R lighthouse:eth" "$tmp/calls.log" || { echo "FAIL: ownership not fixed"; exit 1; }

# Case 2: already imported -> no-op.
mkdir -p "$tmp/lh2/validators" && touch "$tmp/lh2/validators/validator_definitions.yml"
echo -n > "$tmp/calls.log"
PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh2" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh
grep -q "lighthouse" "$tmp/calls.log" && { echo "FAIL: must skip when already imported"; exit 1; }

# Case 3: secrets unavailable -> non-zero exit (gates the vc from starting).
touch "$tmp/SECRETS_FAIL"
if PATH="$tmp/bin:$PATH" DATADIR="$tmp/lh3" LH_BIN="$tmp/bin/lighthouse" bash scripts/fetch_and_import_validator.sh 2>/dev/null; then
  echo "FAIL: expected failure when secrets are unavailable"; exit 1
fi

echo "PASS test_import"
```

**Step 2 — run to fail.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_import.sh
```
Expected: fails — script does not exist.

**Step 3 — implement.** Create `scripts/fetch_and_import_validator.sh`:
```bash
#!/usr/bin/env bash
# Pull validator key material from Secrets Manager and import it into the
# lighthouse datadir. Idempotent: skips if validator_definitions.yml exists.
# A hard failure here (e.g. secrets not yet pushed) is INTENTIONAL — it keeps
# lighthousevalidator.service from ever starting without keys + slashing data.
set -euo pipefail
DATADIR="${DATADIR:-/data/mainnet/lighthouse}"
LH_BIN="${LH_BIN:-/data/bin/lighthouse}"
SECRET_PREFIX="${SECRET_PREFIX:-mainnet-validator}"

if [ -f "$DATADIR/validators/validator_definitions.yml" ]; then
  echo "validator already imported; skipping"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
chmod 700 "$tmp"
mkdir -p "$tmp/keys"

aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/keystore" \
  --query SecretString --output text > "$tmp/keys/voting-keystore.json"
aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/keystore-password" \
  --query SecretString --output text > "$tmp/password.txt"

if ! aws secretsmanager get-secret-value --secret-id "$SECRET_PREFIX/slashing-protection" \
  --query SecretString --output text > "$tmp/interchange.json" 2>/dev/null; then
  echo "WARN: slashing-protection secret unavailable; relying on doppelganger protection only" >&2
  rm -f "$tmp/interchange.json"
fi

"$LH_BIN" account validator import \
  --network mainnet --datadir "$DATADIR" \
  --directory "$tmp/keys" \
  --password-file "$tmp/password.txt" --reuse-password

if [ -f "$tmp/interchange.json" ]; then
  "$LH_BIN" account validator slashing-protection import "$tmp/interchange.json" \
    --network mainnet --datadir "$DATADIR"
fi

chown -R lighthouse:eth "$DATADIR"
echo "validator import complete"
```

Create `scripts/start_reth.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
exec /data/bin/reth node \
  --minimal \
  --chain mainnet \
  --datadir /data/mainnet/reth \
  --authrpc.addr 127.0.0.1 \
  --authrpc.port 8551 \
  --authrpc.jwtsecret /data/shared/jwt.hex \
  --metrics 0.0.0.0:9001 \
  --port 30303
```

Create `scripts/start_lighthouse_bn.sh`:
```bash
#!/usr/bin/env bash
set -euo pipefail
exec /data/bin/lighthouse bn \
  --network mainnet \
  --datadir /data/mainnet/lighthouse \
  --execution-endpoint http://127.0.0.1:8551 \
  --execution-jwt /data/shared/jwt.hex \
  --checkpoint-sync-url https://mainnet.checkpoint.sigp.io \
  --builder http://127.0.0.1:18550 \
  --http --http-address 127.0.0.1 --http-port 5052 \
  --metrics --metrics-address 0.0.0.0 --metrics-port 6064 \
  --port 9000 --quic-port 9001
```

Create `scripts/start_lighthouse_vc.sh` (`FEE_RECIPIENT` comes from the systemd `EnvironmentFile` written by the bootstrap in T9):
```bash
#!/usr/bin/env bash
set -euo pipefail
: "${FEE_RECIPIENT:?FEE_RECIPIENT must be set via /etc/swannynode/validator.env}"
exec /data/bin/lighthouse vc \
  --network mainnet \
  --datadir /data/mainnet/lighthouse \
  --beacon-nodes http://127.0.0.1:5052 \
  --suggested-fee-recipient "$FEE_RECIPIENT" \
  --builder-proposals \
  --enable-doppelganger-protection \
  --metrics --metrics-address 0.0.0.0 --metrics-port 6065
```

Create `scripts/start_mevboost.sh` (relay URLs: flashbots carried over from the old working config; ultrasound and aestus from their published endpoints — T10 includes a pre-deploy verification step for all three):
```bash
#!/usr/bin/env bash
set -euo pipefail
exec /data/bin/mev-boost \
  -mainnet \
  -addr 127.0.0.1:18550 \
  -min-bid 0.05 \
  -relay-check \
  -relays https://0xac6e77dfe25ecd6110b8e780608cce0dab71fdd5ebea22a16c0205200f2f8e2e3ad3b71d3499c54ad14d6c21b41a37ae@boost-relay.flashbots.net,https://0xa1559ace749633b997cb3fdacffb890aeebdb0f5a3b6aaa7eeeaf1a38af0a8fe88b9e4b1f61f236d2e64d95733327a62@relay.ultrasound.money,https://0xa15b52576bcbf1072f4a011c0f99f9fb6c66f3e1ff321f11f461d15e31b1cb359caa092c71bbded0bae5b5ea401aab7e@aestus.live
```

```sh
chmod +x swannynode-mainnet-validator/scripts/*.sh
```

**Step 4 — run to pass.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_import.sh
```
Expected: `PASS test_import`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): validator secret import and client start scripts"
```

---

## Task T7 — systemd units + unit-invariant test

**Files:** create `swannynode-mainnet-validator/config/{reth-init.service,reth.service,lighthousebeacon.service,validator-init.service,lighthousevalidator.service,mevboost.service}`, `swannynode-mainnet-validator/scripts_test/test_units.sh`
**Depends-on:** T1
**Touches:** `swannynode-mainnet-validator/config/*.service`, `swannynode-mainnet-validator/scripts_test/test_units.sh`

**Step 1 — failing test.** Create `scripts_test/test_units.sh`:
```bash
#!/usr/bin/env bash
# Invariants that make the unit graph safe:
# 1. Every unit that touches /data declares RequiresMountsFor=/data
#    (a failed mount must stop clients from initializing on the root disk).
# 2. reth requires reth-init; the vc requires validator-init.
# 3. Long-running services restart automatically.
set -euo pipefail
cd "$(dirname "$0")/../config"

for unit in reth-init.service reth.service lighthousebeacon.service validator-init.service lighthousevalidator.service mevboost.service; do
  [ -f "$unit" ] || { echo "FAIL: missing $unit"; exit 1; }
  grep -q "RequiresMountsFor=/data" "$unit" || { echo "FAIL: $unit lacks RequiresMountsFor=/data"; exit 1; }
done

grep -q "Requires=reth-init.service" reth.service || { echo "FAIL: reth.service must require reth-init"; exit 1; }
grep -q "After=reth-init.service" reth.service || { echo "FAIL: reth.service must order after reth-init"; exit 1; }
grep -q "Requires=validator-init.service" lighthousevalidator.service || { echo "FAIL: vc must require validator-init"; exit 1; }
grep -q "After=validator-init.service" lighthousevalidator.service || { echo "FAIL: vc must order after validator-init"; exit 1; }

for unit in reth.service lighthousebeacon.service lighthousevalidator.service mevboost.service; do
  grep -q "Restart=always" "$unit" || { echo "FAIL: $unit lacks Restart=always"; exit 1; }
done

for unit in reth-init.service validator-init.service; do
  grep -q "Type=oneshot" "$unit" || { echo "FAIL: $unit must be Type=oneshot"; exit 1; }
done

echo "PASS test_units"
```

**Step 2 — run to fail.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_units.sh
```
Expected: `FAIL: missing reth-init.service`.

**Step 3 — implement.** Create `config/reth-init.service`:
```ini
[Unit]
Description=reth one-time snapshot initialization (guarded; no-op if datadir exists)
RequiresMountsFor=/data
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
User=reth
Group=eth
TimeoutStartSec=infinity
ExecStart=/data/scripts/reth_init.sh
StandardOutput=journal
StandardError=journal
SyslogIdentifier=reth-init

[Install]
WantedBy=multi-user.target
```

Create `config/reth.service`:
```ini
[Unit]
Description=reth execution client (mainnet, --minimal)
RequiresMountsFor=/data
Requires=reth-init.service
After=reth-init.service network-online.target
Wants=network-online.target

[Service]
User=reth
Group=eth
ExecStart=/data/scripts/start_reth.sh
Restart=always
RestartSec=30s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=reth

[Install]
WantedBy=multi-user.target
```

Create `config/lighthousebeacon.service`:
```ini
[Unit]
Description=lighthouse beacon node (mainnet)
RequiresMountsFor=/data
Wants=network-online.target
After=network-online.target

[Service]
User=lighthouse
Group=eth
ExecStart=/data/scripts/start_lighthouse_bn.sh
Restart=always
RestartSec=30s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=lighthouse-bn

[Install]
WantedBy=multi-user.target
```

Create `config/validator-init.service`:
```ini
[Unit]
Description=validator one-time key + slashing-protection import from Secrets Manager
RequiresMountsFor=/data
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
User=root
EnvironmentFile=/etc/swannynode/validator.env
ExecStart=/data/scripts/fetch_and_import_validator.sh
StandardOutput=journal
StandardError=journal
SyslogIdentifier=validator-init

[Install]
WantedBy=multi-user.target
```

Create `config/lighthousevalidator.service`:
```ini
[Unit]
Description=lighthouse validator client (mainnet)
RequiresMountsFor=/data
Requires=validator-init.service
After=validator-init.service lighthousebeacon.service

[Service]
User=lighthouse
Group=eth
EnvironmentFile=/etc/swannynode/validator.env
ExecStart=/data/scripts/start_lighthouse_vc.sh
Restart=always
RestartSec=30s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=lighthouse-vc

[Install]
WantedBy=multi-user.target
```

Create `config/mevboost.service`:
```ini
[Unit]
Description=mev-boost (mainnet)
RequiresMountsFor=/data
Wants=network-online.target
After=network-online.target

[Service]
User=mevboost
Group=eth
ExecStart=/data/scripts/start_mevboost.sh
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=mev-boost

[Install]
WantedBy=multi-user.target
```

**Step 4 — run to pass.**
```sh
bash swannynode-mainnet-validator/scripts_test/test_units.sh
```
Expected: `PASS test_units`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): systemd units with mount guards and init ordering"
```

---

## Task T8 — compute.go + wire infra components in main.go

**Files:** create `swannynode-mainnet-validator/{compute.go,compute_test.go}`; modify `swannynode-mainnet-validator/main.go`
**Depends-on:** T1, T2, T3, T4
**Touches:** `swannynode-mainnet-validator/compute.go`, `swannynode-mainnet-validator/compute_test.go`, `swannynode-mainnet-validator/main.go`

**Step 1 — failing test.** Create `compute_test.go`:
```go
package main

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func runFullInfra(t *testing.T) *recordingMocks {
	t.Helper()
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cfg := testCfg()
		net, err := createNetwork(ctx, cfg)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx)
		if err != nil {
			return err
		}
		_, err = createCompute(ctx, cfg, net, sto, id)
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)
	return m
}

func TestComputeInstanceShape(t *testing.T) {
	m := runFullInfra(t)
	inst := m.get("validator")
	require.NotNil(t, inst, "expected EC2 instance 'validator'")
	require.Equal(t, "r8g.xlarge", inst["instanceType"].StringValue())
	require.Equal(t, "ami-mock", inst["ami"].StringValue(), "AMI must come from the SSM AL2023 arm64 parameter")
	root := inst["rootBlockDevice"].ObjectValue()
	require.Equal(t, 30.0, root["volumeSize"].NumberValue())
	require.Equal(t, "gp3", root["volumeType"].StringValue())
}

func TestComputeAttachmentAndEip(t *testing.T) {
	m := runFullInfra(t)
	att := m.get("validator-data-attach")
	require.NotNil(t, att, "expected volume attachment")
	require.Equal(t, "/dev/sdf", att["deviceName"].StringValue())
	require.NotNil(t, m.get("validator-eip-assoc"), "expected EIP association")
}
```

**Step 2 — run to fail.**
```sh
go test ./... -run TestCompute
```
Expected: compile error — `undefined: createCompute`.

**Step 3 — implement.** Create `compute.go`:
```go
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ssm"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Compute struct {
	Instance   *ec2.Instance
	Attachment *ec2.VolumeAttachment
	EipAssoc   *ec2.EipAssociation
}

// createCompute provisions the disposable instance. AMI comes from the SSM
// public parameter for AL2023 arm64; ignoreChanges on ami so routine AMI
// refreshes never force an instance replacement.
func createCompute(ctx *pulumi.Context, cfg StackConfig, net *Network, sto *Storage, id *Identity) (*Compute, error) {
	ami, err := ssm.LookupParameter(ctx, &ssm.LookupParameterArgs{
		Name: "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
	})
	if err != nil {
		return nil, err
	}

	inst, err := ec2.NewInstance(ctx, "validator", &ec2.InstanceArgs{
		Ami:                 pulumi.String(ami.Value),
		InstanceType:        pulumi.String(cfg.InstanceType),
		SubnetId:            pulumi.String(net.SubnetId),
		VpcSecurityGroupIds: pulumi.StringArray{net.SecurityGroup.ID()},
		KeyName:             pulumi.String(cfg.KeyName),
		IamInstanceProfile:  id.Profile.Name,
		RootBlockDevice: &ec2.InstanceRootBlockDeviceArgs{
			VolumeSize: pulumi.Int(30),
			VolumeType: pulumi.String("gp3"),
		},
		Tags: pulumi.StringMap{"Name": pulumi.String("swannynode-mainnet-validator")},
	}, pulumi.IgnoreChanges([]string{"ami"}))
	if err != nil {
		return nil, err
	}

	att, err := ec2.NewVolumeAttachment(ctx, "validator-data-attach", &ec2.VolumeAttachmentArgs{
		DeviceName:                  pulumi.String("/dev/sdf"),
		InstanceId:                  inst.ID(),
		VolumeId:                    sto.Volume.ID(),
		StopInstanceBeforeDetaching: pulumi.Bool(true),
	})
	if err != nil {
		return nil, err
	}

	assoc, err := ec2.NewEipAssociation(ctx, "validator-eip-assoc", &ec2.EipAssociationArgs{
		InstanceId:   inst.ID(),
		AllocationId: net.Eip.ID(),
	})
	if err != nil {
		return nil, err
	}

	return &Compute{Instance: inst, Attachment: att, EipAssoc: assoc}, nil
}
```

Modify `main.go` to wire the infra (replace the whole file):
```go
package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		sc := loadStackConfig(cfg)

		net, err := createNetwork(ctx, sc)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, sc)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx)
		if err != nil {
			return err
		}
		comp, err := createCompute(ctx, sc, net, sto, id)
		if err != nil {
			return err
		}

		ctx.Export("instanceId", comp.Instance.ID())
		ctx.Export("publicIp", net.Eip.PublicIp)
		ctx.Export("dataVolumeId", sto.Volume.ID())
		return nil
	})
}
```

**Step 4 — run to pass.**
```sh
go build ./... && go vet ./... && go test ./...
```
Expected: all `ok`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): EC2 instance, volume attachment, EIP association; wire infra"
```

---

## Task T9 — deploy.go: SSH bootstrap + file delivery + service enablement

**Files:** create `swannynode-mainnet-validator/{deploy.go,deploy_test.go}`; modify `swannynode-mainnet-validator/main.go`
**Depends-on:** T5, T6, T7, T8
**Touches:** `swannynode-mainnet-validator/deploy.go`, `swannynode-mainnet-validator/deploy_test.go`, `swannynode-mainnet-validator/main.go`

**Step 1 — failing test.** Create `deploy_test.go`:
```go
package main

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

func TestDeployBootstrapContent(t *testing.T) {
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		cfg := testCfg()
		net, err := createNetwork(ctx, cfg)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx)
		if err != nil {
			return err
		}
		comp, err := createCompute(ctx, cfg, net, sto, id)
		if err != nil {
			return err
		}
		return deployNode(ctx, cfg, net, sto, comp, pulumi.String("fake-ssh-key").ToStringOutput())
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	boot := m.get("bootstrap")
	require.NotNil(t, boot, "expected remote command 'bootstrap'")
	script := boot["create"].StringValue()
	for _, want := range []string{
		"mount_data.sh",          // volume mounted before anything else
		"install_clients.sh",     // pinned binaries
		"v2.4.1",                 // reth pin flows into bootstrap
		"FEE_RECIPIENT=",         // env file for the vc
		"jwt.hex",                // shared JWT created
		"systemctl daemon-reload",
		"enable --now mevboost reth-init reth lighthousebeacon",
		"systemctl enable validator-init lighthousevalidator", // enabled, NOT started: gated on secrets + old node stopped
	} {
		require.True(t, strings.Contains(script, want), "bootstrap script missing %q", want)
	}
	require.False(t, strings.Contains(script, "enable --now lighthousevalidator"),
		"vc must never auto-start on first deploy")
}
```

**Step 2 — run to fail.**
```sh
go test ./... -run TestDeploy
```
Expected: compile error — `undefined: deployNode`.

**Step 3 — implement.** Create `deploy.go`:
```go
package main

import (
	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// deployNode configures the instance over SSH: copies scripts/units, mounts
// the data volume, installs pinned client binaries, and enables services.
// The validator client is enabled but NOT started — validator-init gates it
// on secrets being present, and first start is a deliberate manual step in
// the migration runbook (after the old instance is stopped).
func deployNode(ctx *pulumi.Context, cfg StackConfig, net *Network, sto *Storage, comp *Compute, sshKey pulumi.StringOutput) error {
	conn := &remote.ConnectionArgs{
		Host:       net.Eip.PublicIp,
		User:       pulumi.String(cfg.SshUser),
		PrivateKey: sshKey,
		Port:       pulumi.Float64Ptr(22),
	}

	copyScripts, err := remote.NewCopyToRemote(ctx, "copy-scripts", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./scripts"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy"),
	}, pulumi.DependsOn([]pulumi.Resource{comp.EipAssoc, comp.Attachment}))
	if err != nil {
		return err
	}

	copyUnits, err := remote.NewCopyToRemote(ctx, "copy-units", &remote.CopyToRemoteArgs{
		Connection: conn,
		Source:     pulumi.NewFileArchive("./config"),
		RemotePath: pulumi.String("/home/" + cfg.SshUser + "/deploy-units"),
	}, pulumi.DependsOn([]pulumi.Resource{comp.EipAssoc, comp.Attachment}))
	if err != nil {
		return err
	}

	bootstrapScript := pulumi.All(sto.Volume.ID().ToStringOutput()).ApplyT(func(vs []interface{}) string {
		volumeId := vs[0].(string)
		home := "/home/" + cfg.SshUser
		return `set -euo pipefail
sudo bash -s <<'BOOTSTRAP'
set -euo pipefail
# --- users & groups (idempotent) ---
getent group eth >/dev/null || groupadd eth
for u in reth lighthouse mevboost; do
  id "$u" >/dev/null 2>&1 || useradd -m -s /bin/bash -g eth "$u"
done
# --- mount data volume ---
install -m 0755 ` + home + `/deploy/scripts/mount_data.sh /usr/local/sbin/mount_data.sh
VOLUME_ID=` + volumeId + ` /usr/local/sbin/mount_data.sh
# --- directory layout ---
mkdir -p /data/bin /data/scripts /data/shared /data/mainnet/reth /data/mainnet/lighthouse
# --- scripts ---
install -m 0755 ` + home + `/deploy/scripts/*.sh /data/scripts/
# --- shared JWT (created once) ---
[ -f /data/shared/jwt.hex ] || (umask 077 && openssl rand -hex 32 > /data/shared/jwt.hex)
chgrp eth /data/shared/jwt.hex && chmod 640 /data/shared/jwt.hex
# --- client binaries (pinned) ---
RETH_VERSION=` + cfg.RethVersion + ` LIGHTHOUSE_VERSION=` + cfg.LighthouseVersion + ` MEVBOOST_VERSION=` + cfg.MevboostVersion + ` /data/scripts/install_clients.sh
# --- ownership ---
chown -R reth:eth /data/mainnet/reth
chown -R lighthouse:eth /data/mainnet/lighthouse
chown root:eth /data/bin /data/scripts /data/shared
# --- validator env ---
mkdir -p /etc/swannynode
printf 'FEE_RECIPIENT=` + cfg.FeeRecipient + `\nSECRET_PREFIX=mainnet-validator\nAWS_DEFAULT_REGION=us-east-2\n' > /etc/swannynode/validator.env
chmod 600 /etc/swannynode/validator.env
# --- systemd units ---
install -m 0644 ` + home + `/deploy-units/config/*.service /etc/systemd/system/ 2>/dev/null || install -m 0644 ` + home + `/deploy-units/*.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now mevboost reth-init reth lighthousebeacon
systemctl enable validator-init lighthousevalidator
BOOTSTRAP
echo bootstrap-complete`
	}).(pulumi.StringOutput)

	_, err = remote.NewCommand(ctx, "bootstrap", &remote.CommandArgs{
		Connection: conn,
		Create:     bootstrapScript,
		Triggers: pulumi.Array{
			pulumi.String(cfg.RethVersion),
			pulumi.String(cfg.LighthouseVersion),
			pulumi.String(cfg.MevboostVersion),
			pulumi.String(cfg.FeeRecipient),
		},
	}, pulumi.DependsOn([]pulumi.Resource{copyScripts, copyUnits}))
	return err
}
```
Note: `CopyToRemote` extracts a `FileArchive` into `RemotePath`; depending on the pulumi-command version the archive contents land as `RemotePath/<files>` or `RemotePath/<dirname>/<files>` — the unit-install line handles both. If `go build` reports the copy resource under a different name in your installed pulumi-command version (older SDKs expose `remote.CopyFile` instead of `CopyToRemote`), upgrade: `go get github.com/pulumi/pulumi-command/sdk@v1`.

Modify `main.go` — add after the `createCompute` call and before the exports:
```go
		sshKey := cfg.RequireSecret("sshKey")
		if err := deployNode(ctx, sc, net, sto, comp, sshKey); err != nil {
			return err
		}
```

**Step 4 — run to pass.**
```sh
go build ./... && go vet ./... && go test ./...
```
Expected: all `ok`.

**Step 5 — commit.**
```sh
git add -A && git commit -m "feat(mainnet-validator): SSH bootstrap deploy with gated validator start"
```

---

## Task T10 — Stack init, preview, README runbook

**Files:** create `swannynode-mainnet-validator/README.md`, `swannynode-mainnet-validator/Pulumi.mainnet.yaml` (via `pulumi stack init`)
**Depends-on:** T2–T9
**Touches:** `swannynode-mainnet-validator/README.md`, `swannynode-mainnet-validator/Pulumi.mainnet.yaml`

**Step 1 — verify relay endpoints and release assets** (pre-deploy sanity; all read-only):
```sh
curl -fsSIo /dev/null -w "%{http_code}\n" https://github.com/paradigmxyz/reth/releases/download/v2.4.1/reth-v2.4.1-aarch64-unknown-linux-gnu.tar.gz          # expect 302
curl -fsSIo /dev/null -w "%{http_code}\n" https://github.com/sigp/lighthouse/releases/download/v8.2.0/lighthouse-v8.2.0-aarch64-unknown-linux-gnu.tar.gz      # expect 302
curl -fsSIo /dev/null -w "%{http_code}\n" https://github.com/flashbots/mev-boost/releases/download/v1.12/mev-boost_1.12_linux_arm64.tar.gz                    # expect 302
curl -fsS https://boost-relay.flashbots.net/eth/v1/builder/status -w "%{http_code}\n"   # expect 200
curl -fsS https://relay.ultrasound.money/eth/v1/builder/status -w "%{http_code}\n"      # expect 200
curl -fsS https://aestus.live/eth/v1/builder/status -w "%{http_code}\n"                 # expect 200
```
If any asset URL 404s, correct the version/asset-name in `scripts/install_clients.sh` and the config defaults in `types.go`, and re-run T5's tests. If a relay check fails, remove that relay from `scripts/start_mevboost.sh`.

**Step 2 — write README.** Create `swannynode-mainnet-validator/README.md`:

```markdown
# swannynode-mainnet-validator

Cost-optimized mainnet Ethereum validator: reth `--minimal` + lighthouse bn/vc + mev-boost
on one Graviton EC2 instance, with a persistent (protected) EBS data volume, keys in
Secrets Manager, and daily DLM snapshots. Design spec:
`docs/specs/2026-07-19-mainnet-validator-design.md`.

## Configuration

| Key | Default | Notes |
|---|---|---|
| `aws:region` | — (set `us-east-2`) | |
| `az` | `us-east-2a` | change + snapshot-restore to move AZ |
| `instanceType` | `r8g.xlarge` | |
| `volumeSizeGb` | `600` | |
| `volumeIops` | `3000` | raise if sync is IOPS-bound |
| `rethVersion` | `v2.4.1` | GitHub release tag |
| `lighthouseVersion` | `v8.2.0` | GitHub release tag |
| `mevboostVersion` | `1.12` | GitHub release tag (no `v` in asset name) |
| `feeRecipient` | **required** | your reward address; verify against a past proposal on beaconcha.in for validator `0xa177d5c2…ee7ec` |
| `keyName` | **required** | existing EC2 key pair name |
| `sshKey` | **required (secret)** | private key matching `keyName` |
| `sshUser` | `ec2-user` | |

## First deploy (migration from the failed host)

1. `pulumi preview` then `pulumi up`. The vc is enabled but will NOT start
   (validator-init fails fast until secrets exist — this is the safety gate).
2. Export the slashing-protection interchange locally (Docker; backup lives in
   `~/validator-backup-2026-07-19/`):
   ```sh
   mkdir -p /tmp/lh-export && tar xzf ~/validator-backup-2026-07-19/lighthouse-mainnet-validators.tar.gz -C /tmp/lh-export
   docker run --rm -v /tmp/lh-export:/root/.lighthouse/mainnet sigp/lighthouse:v8.2.0 \
     lighthouse account validator slashing-protection export /root/.lighthouse/mainnet/interchange.json --network mainnet
   ```
3. Push the three secrets (values never enter Pulumi state):
   ```sh
   V=/tmp/lh-export/validators
   KS=$V/0xa177d5c28a60469bd6fe28255cc510f92ce9efd94bff28e49af97f0f84dd6b69a1262cc0fda6a10d672524f0735ee7ec/voting-keystore.json
   aws secretsmanager put-secret-value --secret-id mainnet-validator/keystore --secret-string file://$KS
   # password: the voting_keystore_password value from $V/validator_definitions.yml
   aws secretsmanager put-secret-value --secret-id mainnet-validator/keystore-password --secret-string "$(grep voting_keystore_password $V/validator_definitions.yml | awk '{print $2}')"
   aws secretsmanager put-secret-value --secret-id mainnet-validator/slashing-protection --secret-string file:///tmp/lh-export/interchange.json
   rm -rf /tmp/lh-export
   ```
4. Wait for sync: `journalctl -u reth-init -f` (snapshot download), then
   `curl -s localhost:5052/eth/v1/node/syncing` on the box until `is_syncing: false`.
5. **Stop the old instance** (`3.145.206.252`) in the EC2 console.
6. Start the validator: `sudo systemctl start validator-init lighthousevalidator`.
   Doppelganger protection delays signing by ~2 epochs (~13 min) — expected.
7. Confirm the first attestation on beaconcha.in for validator `0xa177d5c2…ee7ec`.
8. Terminate the old instance. Update sibling stacks:
   `cd ../alarms && pulumi config set instanceId <new id> && pulumi up`;
   update the prometheus target IP in `../monitoring` and `pulumi up`.
9. Verify `reth node --help | grep -A2 minimal` on the box matches the
   `--minimal` flag used in `/data/scripts/start_reth.sh` (flag verified
   against the pinned release).

## Recovery runbook

| Failure | Action | Expected downtime |
|---|---|---|
| Instance dies | `pulumi up` (new instance; volume + EIP reattach; secrets re-pulled automatically; vc: `systemctl start validator-init lighthousevalidator` after verifying the old instance is gone) | ~15 min |
| Data volume corrupt | Restore latest DLM snapshot to a new volume in the AZ, `pulumi import` or replace the volume resource, `pulumi up`; or delete data and let reth-init re-download | 1–6 h |
| AZ outage | `pulumi config set az us-east-2b`, restore volume from snapshot in new AZ, `pulumi up` | ~1 h |
| Total loss | Fresh `pulumi up` anywhere + secrets already in Secrets Manager + snapshot download | ~6 h |

**Never** run two copies of this stack (or the old host) against the same keys.
```

**Step 3 — stack init + preview.**
```sh
cd swannynode-mainnet-validator
pulumi stack init mainnet
pulumi config set aws:region us-east-2
```
Then set `feeRecipient`, `keyName`, and `sshKey` — **these require user-supplied values; if not available, STOP here, report, and leave the preview to the user.** With them set:
```sh
pulumi preview
```
Expected: ~17 creates (volume, DLM policy + role + attachment, SG, EIP + association, 3 secrets, instance role + inline policy + SSM attachment + profile, instance, volume attachment, 2 copies, bootstrap command), 0 errors. `Protect: true` visible on `validator-data`.

**Step 4 — full local check + commit.**
```sh
go build ./... && go vet ./... && go test ./... \
  && bash scripts_test/test_reth_init.sh && bash scripts_test/test_mount_data.sh \
  && bash scripts_test/test_import.sh && bash scripts_test/test_units.sh
git add -A && git commit -m "docs(mainnet-validator): README runbook, stack config, verified release/relay endpoints"
```

---

## Self-review

- **Spec coverage:** infra-as-code (T2–T4, T8), reth `--minimal` + explicit guarded snapshot init (T5, T7), EBS persistence + protection + DLM (T2), Secrets Manager key flow with vc gating (T4, T6, T7, T9), mount-before-clients invariant (T5, T7), pinned binaries (T5), mev-boost relay refresh (T6, verified T10), metrics port conventions (T6), migration + recovery runbook incl. alarms/monitoring updates (T10). Spec open items resolved: binary installs via plain `remote.Command` (no `node_deployer` dependency); reth flag spelling has a deploy-time verification step (T10 step 9 / README); fee recipient is required config (user-supplied); tailscale dropped (SSM replaces it as backup access).
- **Placeholders:** none — every script/unit/Go file is complete; the two user-supplied values (`feeRecipient`, `sshKey`) are deploy-time configuration, not code gaps.
- **Type consistency:** `StackConfig`, `Network`, `Storage`, `Identity`, `Compute` defined in T1–T4/T8 before use in T8/T9; test helpers (`newMocks`, `testCfg`, `m.get`) defined in T1.
- **Waves:** every Depends-on points to an earlier wave; `main.go` touched only in waves 1, 3, 4; no same-wave file overlap.
