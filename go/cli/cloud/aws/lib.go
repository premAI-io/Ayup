package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	secretsmanagertypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"premai.io/Ayup/go/internal/terror"
	"premai.io/Ayup/go/internal/trace"
	"premai.io/Ayup/go/internal/tui"
)

const (
	secretName          = "ayup-preauth-conf"
	iamRoleName         = "ayup-read-preauth-secrets"
	iamPolicyName       = "ayup-read-preauth-secrets"
	securityGroupName   = "ayup-listen-tcp-50051"
	subnetName          = "ayup"
	cidrBlock           = "10.0.1.0/24"
	vpcName             = "ayup"
	instanceProfileName = "ayup"
	instanceType        = "t2.micro"
	amiID               = "ami-0271107fdf337b99e"
	instanceName        = "ayup"
)

func StartEc2(ctx context.Context, srvPeerId string, preauthConf string) error {
	ctx, span := trace.Span(ctx, "start ec2")
	defer span.End()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithAppID("ayup-cli"))
	if err != nil {
		return terror.Errorf(ctx, "unable to load SDK config, %w", err)
	}
	otelaws.AppendMiddlewares(&cfg.APIOptions)

	smSvc := secretsmanager.NewFromConfig(cfg)
	secretArn, err := createSecret(ctx, smSvc, preauthConf)
	if err != nil {
		return terror.Errorf(ctx, "Failed to create secret: %w", err)
	}

	iamSvc := iam.NewFromConfig(cfg)
	policyArn, err := createIAMPolicy(ctx, iamSvc, secretArn)
	if err != nil {
		return terror.Errorf(ctx, "Failed to create IAM policy: %w", err)
	}

	roleArn, err := createIAMRole(ctx, iamSvc, policyArn)
	if err != nil {
		return terror.Errorf(ctx, "Failed to create IAM role: %w", err)
	}

	ec2Svc := ec2.NewFromConfig(cfg)
	vpcID, err := getOrCreateVPC(ctx, ec2Svc)
	if err != nil {
		return terror.Errorf(ctx, "Failed to get or create VPC: %w", err)
	}

	securityGroupID, err := getOrCreateSecurityGroup(ctx, ec2Svc, vpcID)
	if err != nil {
		return terror.Errorf(ctx, "Failed to get or create security group: %w", err)
	}

	subnetID, err := getOrCreateSubnet(ctx, ec2Svc, vpcID)
	if err != nil {
		return terror.Errorf(ctx, "Failed to get or create subnet: %w", err)
	}

	instanceID, err := launchEC2Instance(ctx, ec2Svc, roleArn, securityGroupID, subnetID)
	if err != nil {
		return terror.Errorf(ctx, "Failed to launch EC2 instance: %w", err)
	}

	instances, err := ec2Svc.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return terror.Errorf(ctx, "Failed to get EC2 instance: %w", err)
	}

	ip4 := *instances.Reservations[0].Instances[0].PublicIpAddress

	fmt.Printf("Successfully launched EC2 instance with ID: %s\n", instanceID)
	fmt.Printf("%s ay login /ip4/%s/tcp/50051/p2p/%s\n",
		tui.TitleStyle.Render("Connect:"), ip4, srvPeerId)

	return nil
}

func getAccountID(ctx context.Context, cfg aws.Config) (string, error) {
	stsSvc := sts.NewFromConfig(cfg)
	identity, err := stsSvc.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return *identity.Account, nil
}

func createIAMPolicy(ctx context.Context, svc *iam.Client, secretArn string) (string, error) {
	policyDocument := fmt.Sprintf(`{
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Action": "secretsmanager:GetSecretValue",
                "Resource": "%s"
            }
        ]
    }`, secretArn)

	// Get current account ID
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}
	accountID, err := getAccountID(ctx, cfg)
	if err != nil {
		return "", err
	}

	// Construct policy ARN
	policyArn := fmt.Sprintf("arn:aws:iam::%s:policy/%s", accountID, iamPolicyName)

	_, err = svc.GetPolicy(ctx, &iam.GetPolicyInput{
		PolicyArn: aws.String(policyArn),
	})
	if err != nil {
		var notFoundErr *iamtypes.NoSuchEntityException
		if errors.As(err, &notFoundErr) {
			// Policy does not exist, create a new one
			createPolicyOutput, err := svc.CreatePolicy(ctx, &iam.CreatePolicyInput{
				PolicyName:     aws.String(iamPolicyName),
				PolicyDocument: aws.String(policyDocument),
			})
			if err != nil {
				return "", err
			}
			return *createPolicyOutput.Policy.Arn, nil
		}
		return "", err
	}

	// Policy exists, create a new policy version
	_, err = svc.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
		PolicyArn:      aws.String(policyArn),
		PolicyDocument: aws.String(policyDocument),
		SetAsDefault:   true,
	})
	if err != nil {
		return "", err
	}

	// List policy versions
	listPolicyVersionsOutput, err := svc.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{
		PolicyArn: aws.String(policyArn),
	})
	if err != nil {
		return "", err
	}

	// Delete the oldest policy version if there are more than 4 versions
	if len(listPolicyVersionsOutput.Versions) > 4 {
		var oldestVersion *iamtypes.PolicyVersion
		for _, version := range listPolicyVersionsOutput.Versions {
			if !version.IsDefaultVersion {
				if oldestVersion == nil || version.CreateDate.Before(*oldestVersion.CreateDate) {
					oldestVersion = &version
				}
			}
		}

		_, err = svc.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
			PolicyArn: aws.String(policyArn),
			VersionId: oldestVersion.VersionId,
		})
		if err != nil {
			return "", err
		}
	}

	return policyArn, nil
}

func createIAMRole(ctx context.Context, svc *iam.Client, policyArn string) (string, error) {
	assumeRolePolicyDocument := `{
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {
                    "Service": "ec2.amazonaws.com"
                },
                "Action": "sts:AssumeRole"
            }
        ]
    }`

	// Check if the role exists
	_, err := svc.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(iamRoleName),
	})
	if err != nil {
		var notFoundErr *iamtypes.NoSuchEntityException
		if errors.As(err, &notFoundErr) {
			// Role does not exist, create a new one
			_, err := svc.CreateRole(ctx, &iam.CreateRoleInput{
				RoleName:                 aws.String(iamRoleName),
				AssumeRolePolicyDocument: aws.String(assumeRolePolicyDocument),
			})
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}

	// Check if the policy is attached to the role
	attachedPolicies, err := svc.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(iamRoleName),
	})
	if err != nil {
		return "", err
	}

	policyAttached := false
	for _, attachedPolicy := range attachedPolicies.AttachedPolicies {
		if *attachedPolicy.PolicyArn == policyArn {
			policyAttached = true
			break
		}
	}

	if !policyAttached {
		// Attach the policy to the role
		_, err = svc.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName:  aws.String(iamRoleName),
			PolicyArn: aws.String(policyArn),
		})
		if err != nil {
			return "", err
		}
	}

	// Check if the instance profile exists
	_, err = svc.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
	})
	if err != nil {
		var notFoundErr *iamtypes.NoSuchEntityException
		if errors.As(err, &notFoundErr) {
			// Instance profile does not exist, create a new one
			_, err = svc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
				InstanceProfileName: aws.String(instanceProfileName),
			})
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}

	// Check if the role is added to the instance profile
	instanceProfile, err := svc.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
	})
	if err != nil {
		return "", err
	}

	roleExistsInProfile := false
	for _, role := range instanceProfile.InstanceProfile.Roles {
		if *role.RoleName == iamRoleName {
			roleExistsInProfile = true
			break
		}
	}

	if !roleExistsInProfile {
		// Add the role to the instance profile
		_, err = svc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
			InstanceProfileName: aws.String(instanceProfileName),
			RoleName:            aws.String(iamRoleName),
		})
		if err != nil {
			return "", err
		}
	}

	return *instanceProfile.InstanceProfile.Arn, nil
}

func getOrCreateVPC(ctx context.Context, svc *ec2.Client) (string, error) {
	// Check if VPC exists
	result, err := svc.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{vpcName},
			},
		},
	})
	if err != nil {
		return "", err
	}

	if len(result.Vpcs) > 0 {
		return *result.Vpcs[0].VpcId, nil
	}

	// Create VPC if it doesn't exist
	createResult, err := svc.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeVpc,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(vpcName),
					},
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	return *createResult.Vpc.VpcId, nil
}

func getOrCreateSecurityGroup(ctx context.Context, svc *ec2.Client, vpcID string) (string, error) {
	// Check if security group exists
	result, err := svc.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("group-name"),
				Values: []string{securityGroupName},
			},
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return "", err
	}

	if len(result.SecurityGroups) > 0 {
		return *result.SecurityGroups[0].GroupId, nil
	}

	// Create security group if it doesn't exist
	createResult, err := svc.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(securityGroupName),
		Description: aws.String("Allow inbound traffic on 50051 for the Ayup CLI"),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		return "", err
	}

	// Authorize inbound traffic on port 50051/tcp
	_, err = svc.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(*createResult.GroupId),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(50051),
				ToPort:     aws.Int32(50051),
				IpRanges: []ec2types.IpRange{
					{
						CidrIp: aws.String("0.0.0.0/0"),
					},
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	return *createResult.GroupId, nil
}

func getOrCreateSubnet(ctx context.Context, svc *ec2.Client, vpcID, cidrBlock, subnetName string) (string, error) {
    // Check if the subnet already exists within the VPC
    describeSubnetsOutput, err := svc.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
        Filters: []ec2types.Filter{
            {
                Name:   aws.String("vpc-id"),
                Values: []string{vpcID},
            },
            {
                Name:   aws.String("cidr-block"),
                Values: []string{cidrBlock},
            },
        },
    })
    if err != nil {
        return "", err
    }

    if len(describeSubnetsOutput.Subnets) > 0 {
        // Subnet exists, return the subnet ID
        subnetID := *describeSubnetsOutput.Subnets[0].SubnetId
        fmt.Printf("Subnet %s already exists in VPC %s.\n", subnetID, vpcID)
        return subnetID, nil
    }

    // Subnet does not exist, create a new one
    createSubnetOutput, err := svc.CreateSubnet(ctx, &ec2.CreateSubnetInput{
        VpcId:     aws.String(vpcID),
        CidrBlock: aws.String(cidrBlock),
    })
    if err != nil {
        return "", err
    }

    subnetID := *createSubnetOutput.Subnet.SubnetId

    // Add Name tag to the subnet
    _, err = svc.CreateTags(ctx, &ec2.CreateTagsInput{
        Resources: []string{subnetID},
        Tags: []ec2types.Tag{
            {
                Key:   aws.String("Name"),
                Value: aws.String(subnetName),
            },
        },
    })
    if err != nil {
        return "", err
    }

    // Modify the Subnet attribute to enable auto-assign public IP addresses
    _, err = svc.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
        SubnetId: aws.String(subnetID),
        MapPublicIpOnLaunch: &ec2types.AttributeBooleanValue{
            Value: aws.Bool(true),
        },
    })
    if err != nil {
        return "", err
    }
    fmt.Printf("Enabled auto-assign public IP on Subnet %s\n", subnetID)

    // Check if an Internet Gateway already exists for the VPC
    describeIGWsOutput, err := svc.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
        Filters: []ec2types.Filter{
            {
                Name:   aws.String("attachment.vpc-id"),
                Values: []string{vpcID},
            },
        },
    })
    if err != nil {
        return "", err
    }

    var igwID string
    if len(describeIGWsOutput.InternetGateways) > 0 {
        // Internet Gateway exists, get its ID
        igwID = *describeIGWsOutput.InternetGateways[0].InternetGatewayId
        fmt.Printf("Internet Gateway %s already exists for VPC %s.\n", igwID, vpcID)
    } else {
        // Create a new Internet Gateway
        createIGWOutput, err := svc.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
        if err != nil {
            return "", err
        }
        igwID = *createIGWOutput.InternetGateway.InternetGatewayId

        // Attach the Internet Gateway to the VPC
        _, err = svc.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
            InternetGatewayId: aws.String(igwID),
            VpcId:             aws.String(vpcID),
        })
        if err != nil {
            return "", err
        }
        fmt.Printf("Created and attached Internet Gateway %s to VPC %s.\n", igwID, vpcID)
    }

    // Create a Route Table
    createRTOutput, err := svc.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
        VpcId: aws.String(vpcID),
    })
    if err != nil {
        return "", err
    }
    rtID := *createRTOutput.RouteTable.RouteTableId

    // Create a route to the Internet Gateway in the Route Table
    _, err = svc.CreateRoute(ctx, &ec2.CreateRouteInput{
        RouteTableId:         aws.String(rtID),
        DestinationCidrBlock: aws.String("0.0.0.0/0"),
        GatewayId:            aws.String(igwID),
    })
    if err != nil {
        return "", err
    }
    fmt.Printf("Created route to Internet Gateway %s in Route Table %s.\n", igwID, rtID)

    // Associate the Route Table with the Subnet
    _, err = svc.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
        RouteTableId: aws.String(rtID),
        SubnetId:     aws.String(subnetID),
    })
    if err != nil {
        return "", err
    }
    fmt.Printf("Associated Route Table %s with Subnet %s.\n", rtID, subnetID)

    return subnetID, nil
}

func createSecret(ctx context.Context, svc *secretsmanager.Client, secretValue string) (string, error) {
	// Check if the secret already exists
	_, err := svc.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(secretName),
	})

	if err == nil {
		// Secret exists, update it
		output, err := svc.UpdateSecret(ctx, &secretsmanager.UpdateSecretInput{
			SecretId:     aws.String(secretName),
			SecretString: aws.String(secretValue),
		})
		if err != nil {
			return "", err
		}
		return *output.ARN, nil
	}

	var notFoundErr *secretsmanagertypes.ResourceNotFoundException
	if errors.As(err, &notFoundErr) {
		// Secret does not exist, create a new one
		output, err := svc.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
			Name:         aws.String(secretName),
			SecretString: aws.String(secretValue),
		})
		if err != nil {
			return "", err
		}
		return *output.ARN, nil
	}

	return "", err
}

func launchEC2Instance(ctx context.Context, svc *ec2.Client, roleArn, securityGroupID, subnetId string) (string, error) {
	// Check if an instance with the same name tag is already running
	describeInstancesOutput, err := svc.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{instanceName},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending", "stopping", "stopped"},
			},
		},
	})
	if err != nil {
		return "", err
	}

	for _, reservation := range describeInstancesOutput.Reservations {
		for _, instance := range reservation.Instances {
			instanceID := *instance.InstanceId
			instanceState := instance.State.Name

			switch instanceState {
			case "running":
				fmt.Printf("Instance %s is already running. Restarting it...\n", instanceID)
				// Stop the instance
				_, err := svc.StopInstances(ctx, &ec2.StopInstancesInput{
					InstanceIds: []string{instanceID},
				})
				if err != nil {
					return "", err
				}

				// Wait until the instance is stopped
				stoppedWaiter := ec2.NewInstanceStoppedWaiter(svc)
				err = stoppedWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}

				// Start the instance
				_, err = svc.StartInstances(ctx, &ec2.StartInstancesInput{
					InstanceIds: []string{instanceID},
				})
				if err != nil {
					return "", err
				}

				// Wait until the instance is running
				runningWaiter := ec2.NewInstanceRunningWaiter(svc)
				err = runningWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}

				// Wait until the instance status is OK
				err = waitForInstanceStatusOK(ctx, svc, instanceID)
				if err != nil {
					return "", err
				}

				return instanceID, nil
			case "stopped":
				fmt.Printf("Instance %s is stopped. Starting it...\n", instanceID)
				_, err := svc.StartInstances(ctx, &ec2.StartInstancesInput{
					InstanceIds: []string{instanceID},
				})
				if err != nil {
					return "", err
				}
				// Wait until the instance is running
				runningWaiter := ec2.NewInstanceRunningWaiter(svc)
				err = runningWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}
				// Wait until the instance status is OK
				err = waitForInstanceStatusOK(ctx, svc, instanceID)
				if err != nil {
					return "", err
				}
				return instanceID, nil
			case "stopping":
				fmt.Printf("Instance %s is stopping. Waiting for it to stop...\n", instanceID)
				stoppedWaiter := ec2.NewInstanceStoppedWaiter(svc)
				err := stoppedWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}
				fmt.Printf("Instance %s is now stopped. Starting it...\n", instanceID)
				_, err = svc.StartInstances(ctx, &ec2.StartInstancesInput{
					InstanceIds: []string{instanceID},
				})
				if err != nil {
					return "", err
				}
				// Wait until the instance is running
				runningWaiter := ec2.NewInstanceRunningWaiter(svc)
				err = runningWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}
				// Wait until the instance status is OK
				err = waitForInstanceStatusOK(ctx, svc, instanceID)
				if err != nil {
					return "", err
				}
				return instanceID, nil
			case "pending":
				fmt.Printf("Instance %s is pending. Waiting for it to run...\n", instanceID)
				runningWaiter := ec2.NewInstanceRunningWaiter(svc)
				err := runningWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{instanceID},
				}, 5*time.Minute)
				if err != nil {
					return "", err
				}
				// Wait until the instance status is OK
				err = waitForInstanceStatusOK(ctx, svc, instanceID)
				if err != nil {
					return "", err
				}
				return instanceID, nil
			}
		}
	}

	// No existing instance found, launch a new one
	runResult, err := svc.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String(amiID),
		InstanceType: ec2types.InstanceTypeT2Micro,
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		IamInstanceProfile: &ec2types.IamInstanceProfileSpecification{
			Arn: aws.String(roleArn),
		},
		SecurityGroupIds: []string{securityGroupID},
		SubnetId:         &subnetId,
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(instanceName),
					},
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	instanceID := *runResult.Instances[0].InstanceId

	// Wait until the instance is running
	runningWaiter := ec2.NewInstanceRunningWaiter(svc)
	err = runningWaiter.Wait(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 5*time.Minute)
	if err != nil {
		return "", err
	}

	// Wait until the instance status is OK
	err = waitForInstanceStatusOK(ctx, svc, instanceID)
	if err != nil {
		return "", err
	}

	return instanceID, nil
}

func waitForInstanceStatusOK(ctx context.Context, svc *ec2.Client, instanceID string) error {
	// Create a context with a 5-minute timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var lastStatus *ec2types.InstanceStatus

	for {
		select {
		case <-timeoutCtx.Done():
			fmt.Printf("Timed out waiting for instance %s status to be OK. Last status: %+v\n", instanceID, lastStatus)
			return fmt.Errorf("timed out waiting for instance %s status to be OK", instanceID)
		default:
			descResult, err := svc.DescribeInstanceStatus(ctx, &ec2.DescribeInstanceStatusInput{
				InstanceIds: []string{instanceID},
			})
			if err != nil {
				return err
			}

			if len(descResult.InstanceStatuses) > 0 {
				status := descResult.InstanceStatuses[0]
				lastStatus = &status
				if status.InstanceStatus.Status == ec2types.SummaryStatusOk && status.SystemStatus.Status == ec2types.SummaryStatusOk {
					fmt.Printf("Instance %s status is OK.\n", instanceID)
					return nil
				}
			}

			fmt.Printf("Waiting for instance %s status to be OK...\n", instanceID)
			time.Sleep(10 * time.Second)
		}
	}
}
