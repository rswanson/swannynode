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
		_, err := createIdentity(ctx, testCfg())
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
		_, err := createIdentity(ctx, testCfg())
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	rp := m.get("validator-secrets-read")
	require.NotNil(t, rp, "expected inline role policy 'validator-secrets-read'")
	policy := rp["policy"].StringValue()
	require.True(t, strings.Contains(policy, "secretsmanager:GetSecretValue"))
	require.False(t, strings.Contains(policy, "\"*\""), "policy must not grant on all resources")
}

// TestIdentityLookupPathDoesNotCreateSecrets covers cfg.CreateSecrets == false,
// the exact configuration the migration stack runs in production (it runs
// parallel to the live stack and must look up rather than create, per the
// comment on createIdentity). This path had zero test coverage before: a
// regression here would either collide with the live stack's secret names on
// `pulumi up`, or adopt key material that a later `pulumi destroy` of the
// migration stack would schedule for deletion.
func TestIdentityLookupPathDoesNotCreateSecrets(t *testing.T) {
	cfg := testCfg()
	cfg.CreateSecrets = false

	m := newMocks()
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := createIdentity(ctx, cfg)
		return err
	}, pulumi.WithMocks("swannynode-mainnet-validator", "mainnet", m))
	require.NoError(t, err)

	for _, n := range []string{"keystore", "keystore-password", "slashing-protection"} {
		sec := m.get("validator-secret-" + n)
		require.Nil(t, sec, "lookup path must not register a validator-secret-"+n+" resource")
	}

	rp := m.get("validator-secrets-read")
	require.NotNil(t, rp, "expected inline role policy 'validator-secrets-read' even on the lookup path")
	policy := rp["policy"].StringValue()
	require.True(t, strings.Contains(policy, "secretsmanager:GetSecretValue"))
	require.False(t, strings.Contains(policy, "\"*\""), "policy must not grant on all resources")
}
