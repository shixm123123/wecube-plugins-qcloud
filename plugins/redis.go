package plugins

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	redis "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/redis/v20180412"
)

const (
	REDIS_STATUS_RUNNING  = 4
	REDIS_STATUS_ISOLATED = 5
)

var RedisActions = make(map[string]Action)

func init() {
	RedisActions["create"] = new(RedisCreateAction)
	RedisActions["terminate"] = new(RedisTerminateAction)
}

func CreateRedisClient(region, secretId, secretKey string) (client *redis.Client, err error) {
	credential := common.NewCredential(secretId, secretKey)

	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = "redis.tencentcloudapi.com"

	return redis.NewClient(credential, region, clientProfile)
}

type RedisInputs struct {
	Inputs []RedisInput `json:"inputs,omitempty"`
}

type RedisInput struct {
	Guid           string `json:"guid,omitempty"`
	ProviderParams string `json:"provider_params,omitempty"`
	TypeID         uint64 `json:"type_id,omitempty"`
	MemSize        uint64 `json:"mem_size,omitempty"`
	GoodsNum       uint64 `json:"goods_num,omitempty"`
	Period         uint64 `json:"period,omitempty"`
	Password       string `json:"password,omitempty"`
	BillingMode    int64  `json:"billing_mode,omitempty"`
	VpcID          string `json:"vpc_id,omitempty"`
	SubnetID       string `json:"subnet_id,omitempty"`
	ID             string `json:"id,omitempty"`
}

type RedisOutputs struct {
	Outputs []RedisOutput `json:"outputs,omitempty"`
}

type RedisOutput struct {
	RequestId string `json:"request_id,omitempty"`
	Guid      string `json:"guid,omitempty"`
	DealID    string `json:"deal_id,omitempty"`
	TaskID    int64  `json:"task_id,omitempty"`
	ID        string `json:"id,omitempty"`
}

type RedisPlugin struct {
}

func (plugin *RedisPlugin) GetActionByName(actionName string) (Action, error) {
	action, found := RedisActions[actionName]

	if !found {
		return nil, fmt.Errorf("Redis plugin,action = %s not found", actionName)
	}

	return action, nil
}

type RedisCreateAction struct {
}

func (action *RedisCreateAction) ReadParam(param interface{}) (interface{}, error) {
	var inputs RedisInputs
	err := UnmarshalJson(param, &inputs)
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func (action *RedisCreateAction) CheckParam(input interface{}) error {
	rediss, ok := input.(RedisInputs)
	if !ok {
		return fmt.Errorf("RedisCreateAction:input type=%T not right", input)
	}

	for _, redis := range rediss.Inputs {
		if redis.GoodsNum == 0 {
			return errors.New("RedisCreateAction input goodsnum is invalid")
		}
		if redis.Password == "" {
			return errors.New("RedisCreateAction input password is empty")
		}
		if redis.BillingMode != 0 && redis.BillingMode != 1 {
			return errors.New("RedisCreateAction input password is invalid")
		}
	}

	return nil
}

func (action *RedisCreateAction) createRedis(redisInput *RedisInput) (*RedisOutput, error) {
	paramsMap, err := GetMapFromProviderParams(redisInput.ProviderParams)
	client, _ := CreateRedisClient(paramsMap["Region"], paramsMap["SecretID"], paramsMap["SecretKey"])

	zonemap, err := GetAvaliableZoneInfo(paramsMap["Region"], paramsMap["SecretID"], paramsMap["SecretKey"])
	if err != nil {
		return nil, err
	}

	request := redis.NewCreateInstancesRequest()
	if _, found := zonemap[paramsMap["AvailableZone"]]; !found {
		err = errors.New("not found available zone info")
		return nil, err
	}

	zoneid := uint64(zonemap[paramsMap["AvailableZone"]])
	request.ZoneId = &zoneid
	request.TypeId = &redisInput.TypeID
	request.MemSize = &redisInput.MemSize
	request.GoodsNum = &redisInput.GoodsNum
	request.Period = &redisInput.Period
	request.Password = &redisInput.Password
	request.BillingMode = &redisInput.BillingMode

	if (*redisInput).VpcID != "" {
		request.VpcId = &redisInput.VpcID
	}

	if (*redisInput).SubnetID != "" {
		request.SubnetId = &redisInput.SubnetID
	}

	response, err := client.CreateInstances(request)
	if err != nil {
		logrus.Errorf("failed to create redis, error=%s", err)
		return nil, err
	}

	instanceid, err := action.waitForRedisInstancesCreationToFinish(client, *response.Response.DealId)
	if err != nil {
		return nil, err
	}

	output := RedisOutput{}
	output.RequestId = *response.Response.RequestId
	output.Guid = redisInput.Guid
	output.DealID = *response.Response.DealId
	output.ID = instanceid

	return &output, nil
}

func (action *RedisCreateAction) Do(input interface{}) (interface{}, error) {
	rediss, _ := input.(RedisInputs)
	outputs := RedisOutputs{}
	for _, redis := range rediss.Inputs {
		redisOutput, err := action.createRedis(&redis)
		if err != nil {
			return nil, err
		}
		outputs.Outputs = append(outputs.Outputs, *redisOutput)
	}

	logrus.Infof("all rediss = %v are created", rediss)
	return &outputs, nil
}

func (action *RedisCreateAction) waitForRedisInstancesCreationToFinish(client *redis.Client, dealid string) (string, error) {
	request := redis.NewDescribeInstanceDealDetailRequest()
	request.DealIds = append(request.DealIds, &dealid)
	var instanceids string
	count := 0
	for {
		response, err := client.DescribeInstanceDealDetail(request)
		if err != nil {
			return "", err
		}

		if len(response.Response.DealDetails) == 0 {
			return "", fmt.Errorf("the redis (dealid = %v) not found", dealid)
		}

		if *response.Response.DealDetails[0].Status == REDIS_STATUS_RUNNING {
			for _, instanceid := range response.Response.DealDetails[0].InstanceIds {
				if instanceids == "" {
					instanceids = *instanceid
				} else {
					instanceids = instanceids + "," + *instanceid
				}
			}
			return instanceids, nil
		}

		time.Sleep(10 * time.Second)
		count++
		if count >= 20 {
			return "", errors.New("waitForRedisInstancesCreationToFinish timeout")
		}
	}
}

type RedisTerminateAction struct {
}

func (action *RedisTerminateAction) ReadParam(param interface{}) (interface{}, error) {
	var inputs RedisInputs
	err := UnmarshalJson(param, &inputs)
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func (action *RedisTerminateAction) CheckParam(input interface{}) error {
	rediss, ok := input.(RedisInputs)
	if !ok {
		return fmt.Errorf("redisTerminateAtion:input type=%T not right", input)
	}

	for _, redis := range rediss.Inputs {
		if redis.ID == "" {
			return errors.New("RedisTerminateAtion input id is empty")
		}
		if redis.Password == "" {
			return errors.New("RedisTerminateAtion input Password is empty")
		}
	}
	return nil
}

func (action *RedisTerminateAction) terminateRedis(redisInput *RedisInput) (*RedisOutput, error) {
	paramsMap, err := GetMapFromProviderParams(redisInput.ProviderParams)
	client, _ := CreateRedisClient(paramsMap["Region"], paramsMap["SecretID"], paramsMap["SecretKey"])

	request := redis.NewClearInstanceRequest()
	request.InstanceId = &redisInput.ID
	request.Password = &redisInput.Password

	response, err := client.ClearInstance(request)
	if err != nil {
		return nil, fmt.Errorf("Failed to ClearInstance(InstanceId=%v), error=%s", redisInput.ID, err)
	}
	output := RedisOutput{}
	output.RequestId = *response.Response.RequestId
	output.Guid = redisInput.Guid
	output.TaskID = *response.Response.TaskId

	return &output, nil
}

func (action *RedisTerminateAction) Do(input interface{}) (interface{}, error) {
	rediss, _ := input.(RedisInputs)
	outputs := RedisOutputs{}
	for _, redis := range rediss.Inputs {
		output, err := action.terminateRedis(&redis)
		if err != nil {
			return nil, err
		}
		outputs.Outputs = append(outputs.Outputs, *output)
	}

	return &outputs, nil
}

func CreateDescribeZonesClient(region, secretId, secretKey string) (client *cvm.Client, err error) {
	credential := common.NewCredential(secretId, secretKey)

	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = "cvm.tencentcloudapi.com"

	return cvm.NewClient(credential, region, clientProfile)
}

func GetAvaliableZoneInfo(region, secretid, secretkey string) (map[string]int, error) {
	ZoneMap := make(map[string]int)
	//获取redis zoneid
	zonerequest := cvm.NewDescribeZonesRequest()
	zoneClient, _ := CreateDescribeZonesClient(region, secretid, secretkey)
	zoneresponse, err := zoneClient.DescribeZones(zonerequest)
	if err != nil {
		logrus.Errorf("failed to get availablezone list, error=%s", err)
		return nil, err
	}

	if *zoneresponse.Response.TotalCount == 0 {
		err = errors.New("availablezone count is zero")
		return nil, err
	}

	for _, zoneinfo := range zoneresponse.Response.ZoneSet {
		if *zoneinfo.ZoneState == "AVAILABLE" {
			ZoneMap[*zoneinfo.Zone], _ = strconv.Atoi(*zoneinfo.ZoneId)
		}
	}

	return ZoneMap, nil
}
