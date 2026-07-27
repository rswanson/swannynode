package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
)

// bootstrapResourceFor runs the deploy program for a given stack config and
// returns the raw recorded inputs of the "bootstrap" remote.Command resource,
// for assertions that need more than the rendered Create script (e.g.
// Triggers).
func bootstrapResourceFor(t *testing.T, cfg StackConfig) resource.PropertyMap {
	t.Helper()
	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		net, err := createNetwork(ctx, cfg)
		if err != nil {
			return err
		}
		sto, err := createStorage(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := createIdentity(ctx, cfg)
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
	return boot
}

// bootstrapFor renders the remote bootstrap script for a given stack config.
func bootstrapFor(t *testing.T, cfg StackConfig) string {
	t.Helper()
	return bootstrapResourceFor(t, cfg)["create"].StringValue()
}

func TestDeployBootstrapContent(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	for _, want := range []string{
		"install_clients.sh", // pinned binaries
		"v2.4.1",             // reth pin flows into bootstrap
		"FEE_RECIPIENT=",     // env file for the vc
		"jwt.hex",            // shared JWT created
		"systemctl daemon-reload",
		"systemctl enable mevboost reth-init reth lighthousebeacon",
		// --no-block: reth-init is a oneshot with infinite timeout; a blocking
		// start would hang the bootstrap for the whole snapshot download
		"systemctl start --no-block mevboost reth-init reth lighthousebeacon",
		"awscli-exe-linux-aarch64.zip",            // Ubuntu images lack aws cli; validator-init needs it
		"systemctl enable validator-init",         // pre-stage keys so cutover is a fast stop/start
		"MOUNT=/validator",                        // slashing protection on its own EBS volume
		"VALIDATOR_DATADIR=/validator/lighthouse", // vc + validator-init both read this
		"systemctl disable lighthousevalidator",
	} {
		require.True(t, strings.Contains(script, want), "bootstrap script missing %q", want)
	}
	require.False(t, strings.Contains(script, "enable --now"),
		"blocking enable --now must not be used; vc must never auto-start on first deploy")
	require.False(t, strings.Contains(script, "start --no-block lighthousevalidator"),
		"vc must never auto-start on first deploy")

	// Regression: this host can be built while ANOTHER host is still signing for
	// the same keys. An enabled vc unit would start a second signer on reboot
	// and get the validator slashed, so it must be left explicitly disabled.
	require.False(t, strings.Contains(script, "systemctl enable validator-init lighthousevalidator"),
		"vc must NOT be enabled at bootstrap: a reboot during a parallel migration would start a second signer")
}

func TestDeployMountsInstanceStoreWhenConfigured(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "mount_instance_store.sh"),
		"instance-store stacks must mount /data from ephemeral NVMe")
	require.False(t, strings.Contains(script, "MOUNT=/data /usr/local/sbin/mount_data.sh"),
		"instance-store stacks must not mount /data from EBS")
}

func TestDeployMountsEbsChainDataWhenConfigured(t *testing.T) {
	script := bootstrapFor(t, ebsCfg())
	require.True(t, strings.Contains(script, "MOUNT=/data /usr/local/sbin/mount_data.sh"),
		"EBS stacks must mount /data from the chain-data volume")
	require.False(t, strings.Contains(script, "mount_instance_store.sh"),
		"EBS stacks must not touch the instance-store mount path")
}

// Regression for the hazard `pulumi preview --stack mainnet` exposed: an
// unconditional /validator datadir repoints a LIVE validator at an empty
// volume. It keeps attesting off in-memory state, then on the next restart
// validator-init re-imports a stale interchange from Secrets Manager and signs
// against a slashing DB missing everything since the export.
func TestDeployEbsStackKeepsValidatorDataDirInPlace(t *testing.T) {
	script := bootstrapFor(t, ebsCfg())
	require.True(t, strings.Contains(script, "VALIDATOR_DATADIR=/data/mainnet/lighthouse"),
		"EBS stacks must keep the historical validator datadir")
	require.False(t, strings.Contains(script, "/validator/lighthouse"),
		"EBS stacks must never reference the instance-store validator path")
	require.False(t, strings.Contains(script, "MOUNT=/validator"),
		"EBS stacks must not provision or mount a separate validator volume")
}

func TestDeployInstanceStoreSplitsValidatorDataDir(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "VALIDATOR_DATADIR=/validator/lighthouse"),
		"instance-store stacks must keep validator state off ephemeral /data")
	require.True(t, strings.Contains(script, "MOUNT=/validator"),
		"instance-store stacks must mount the validator volume")
}

// Regression for Finding 4.1: the bootstrap command's own Triggers only
// contained client versions/feeRecipient, so a REPLACEMENT instance (new
// instance ID, same stable EIP so SSH looks identical) never re-ran the
// bootstrap. That was harmless when /data was EBS (it just reattached with
// everything intact) but is fatal now that /data can be ephemeral NVMe: the
// replacement comes up with /data completely blank and no self-heal ever
// runs. The instance ID must be a trigger so a replacement always
// re-bootstraps from scratch.
func TestBootstrapTriggersIncludeInstanceID(t *testing.T) {
	boot := bootstrapResourceFor(t, testCfg())
	triggers, ok := boot["triggers"]
	require.True(t, ok, "bootstrap command must declare triggers")
	require.True(t, triggers.IsArray(), "triggers must be an array")

	// The mock resource provider returns "<name>_id" as the id of every
	// resource; the compute.go instance resource is named "validator".
	const wantInstanceID = "validator_id"
	found := false
	for _, v := range triggers.ArrayValue() {
		if v.IsString() && v.StringValue() == wantInstanceID {
			found = true
		}
	}
	require.True(t, found,
		"bootstrap triggers must include the instance ID, or a replacement instance never re-bootstraps and comes up with a blank ephemeral /data")
}

// Regression for Finding 6: `systemctl disable lighthousevalidator` must only
// ever run on a genuinely fresh host. The bootstrap re-runs whenever its
// Triggers change (e.g. a routine client version bump), and by that point the
// host may be the LIVE signer with a human having deliberately enabled the
// unit via cutover_validator.sh. An unconditional disable would silently
// strip that unit's boot persistence — the validator would then be down,
// silently, on the next reboot. The disable must be guarded by a marker so it
// fires at most once per instance.
func TestDeployDisableIsFirstBootGuardedNotUnconditional(t *testing.T) {
	script := bootstrapFor(t, testCfg())

	require.True(t,
		strings.Contains(script, "if [ ! -f /etc/swannynode/.vc-first-boot-done ]; then\n  systemctl disable lighthousevalidator"),
		"the disable must be gated behind a first-boot marker check, not run unconditionally")
	require.True(t, strings.Contains(script, "touch /etc/swannynode/.vc-first-boot-done"),
		"the marker must be written so subsequent bootstrap re-runs skip the disable")

	// A bare, unguarded disable line (no leading marker check) must not be
	// present — this is what the original defect looked like.
	require.False(t,
		strings.Contains(script, "\nsystemctl disable lighthousevalidator >/dev/null 2>&1 || true\n"),
		"the disable must not appear unguarded at the top level of the script")
}

// Regression for Finding 1: /validator is mounted `nofail`, so a failed EBS
// attach/mount leaves it as an empty directory on the root disk.
// validator-init would then find no sentinel and re-import a stale
// slashing-protection interchange from Secrets Manager, and the vc would sign
// against a DB missing everything since that export. Both units must refuse
// to run unless /validator is a real mount point — but ONLY in instance-store
// mode, since EBS stacks have no /validator at all and a static condition
// there would permanently block the validator client (the trap called out in
// the finding).
func TestDeployValidatorMountConditionInstanceStoreOnly(t *testing.T) {
	withStore := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(withStore, "ConditionPathIsMountPoint=/validator"),
		"instance-store stacks must refuse to run validator-init/vc when /validator isn't actually mounted")
	require.True(t,
		strings.Contains(withStore, "/etc/systemd/system/validator-init.service.d/10-validator-mount.conf"),
		"the condition must be delivered as a drop-in for validator-init")
	require.True(t,
		strings.Contains(withStore, "/etc/systemd/system/lighthousevalidator.service.d/10-validator-mount.conf"),
		"the condition must be delivered as a drop-in for the vc")

	ebs := bootstrapFor(t, ebsCfg())
	require.False(t, strings.Contains(ebs, "ConditionPathIsMountPoint=/validator"),
		"EBS stacks have no /validator volume at all; a static mount condition would permanently block the validator client")
	require.False(t, strings.Contains(ebs, "10-validator-mount.conf"),
		"EBS stacks must not write the instance-store-only drop-in")
}

// Regression for Finding 4.2/4.3: scripts were installed only to
// /data/scripts, which is wiped along with the rest of ephemeral /data on an
// instance-store stop/start. Systemd ExecStart paths must point somewhere on
// the root volume that survives that wipe, or a self-healed /data still has
// no working units.
func TestDeployScriptsInstalledToRootVolume(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "/opt/swannynode/scripts"),
		"scripts must be installed to a root-volume path that survives an instance-store wipe of /data")

	units := []string{
		"reth-init.service", "reth.service", "lighthousebeacon.service",
		"validator-init.service", "lighthousevalidator.service", "mevboost.service",
	}
	for _, unit := range units {
		b, err := os.ReadFile(filepath.Join("config", unit))
		require.NoError(t, err, "reading %s", unit)
		content := string(b)
		require.False(t, strings.Contains(content, "/data/scripts"),
			"%s must not ExecStart from /data/scripts, which is wiped on an instance-store stop/start", unit)
		require.True(t, strings.Contains(content, "ExecStart=/opt/swannynode/scripts/"),
			"%s must ExecStart from the root-volume scripts path", unit)
	}
}

// Regression for Finding 4.3: a stop/start (not just an instance replacement)
// wipes ephemeral /data, and nothing previously re-provisioned it — the host
// came back with no mounts, no binaries, no working units. A boot-time oneshot
// must detect this and self-heal before reth-init (the first unit that needs
// /data) runs.
func TestDeployReprovisionUnitEnabledAndOrderedBeforeRethInit(t *testing.T) {
	script := bootstrapFor(t, testCfg())
	require.True(t, strings.Contains(script, "systemctl enable data-reprovision"),
		"the reprovision oneshot must be enabled so it runs on every boot, including after a stop/start")

	b, err := os.ReadFile(filepath.Join("config", "data-reprovision.service"))
	require.NoError(t, err)
	unit := string(b)
	require.True(t, strings.Contains(unit, "Before=reth-init.service"),
		"data-reprovision must be ordered before reth-init, the first unit that needs /data")

	b, err = os.ReadFile(filepath.Join("config", "reth-init.service"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(b), "Requires=data-reprovision.service"),
		"reth-init must actually depend on data-reprovision, not just be orderable after it, or the two can race with no dependency pulling reprovision in first")
}
