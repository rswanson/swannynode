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
