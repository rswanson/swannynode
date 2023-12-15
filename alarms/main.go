package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v6/go/aws/sns"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// Load the stack configuration variables
		cfg := config.New(ctx, "")
		instanceId := cfg.Require("instanceId")
		email := cfg.Require("email")

		// Create an SNS topic that we can publish alarm actions to
		topic, err := sns.NewTopic(ctx, "alarmTopic", nil)
		if err != nil {
			return err
		}

		// Subscribe an email address to the SNS topic
		_, err = sns.NewTopicSubscription(ctx, "emailSubscription", &sns.TopicSubscriptionArgs{
			Topic:    topic.Arn,
			Protocol: pulumi.String("email"),
			Endpoint: pulumi.String(email),
		})
		if err != nil {
			return err
		}

		// Define the metric we want to set an alarm on
		statusCheckFailedMetric, err := cloudwatch.NewMetricAlarm(ctx, "statusCheckFailedAlarm", &cloudwatch.MetricAlarmArgs{
			Name:               pulumi.String("swannynodeStatusCheckFailedAlarm"),
			ComparisonOperator: pulumi.String("GreaterThanOrEqualToThreshold"),
			EvaluationPeriods:  pulumi.Int(1),
			MetricName:         pulumi.String("StatusCheckFailed"),
			Namespace:          pulumi.String("AWS/EC2"),
			Period:             pulumi.Int(300), // in seconds
			Statistic:          pulumi.String("Maximum"),
			Threshold:          pulumi.Float64(1),
			ActionsEnabled:     pulumi.Bool(true),
			Dimensions: pulumi.StringMap{
				"InstanceId": pulumi.String(instanceId),
			},
			AlarmActions: pulumi.Array{
				topic.Arn,
			},
		})

		if err != nil {
			return err
		}

		// Export the ARNs of the resources
		ctx.Export("topicArn", topic.Arn)
		ctx.Export("alarmArn", statusCheckFailedMetric.Arn)

		return nil
	})
}
