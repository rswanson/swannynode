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
