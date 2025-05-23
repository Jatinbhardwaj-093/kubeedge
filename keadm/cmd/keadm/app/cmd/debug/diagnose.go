package debug

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"

	"github.com/kubeedge/api/apis/common/constants"
	"github.com/kubeedge/api/apis/componentconfig/edgecore/v1alpha2"
	"github.com/kubeedge/kubeedge/common/types"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao"
	"github.com/kubeedge/kubeedge/keadm/cmd/keadm/app/cmd/common"
	"github.com/kubeedge/kubeedge/keadm/cmd/keadm/app/cmd/util"
	"github.com/kubeedge/kubeedge/pkg/util/files"
)

var (
	edgeDiagnoseLongDescription = `keadm debug diagnose command Diagnose relevant information at edge nodes
`
	edgeDiagnoseShortDescription = `Diagnose relevant information at edge nodes`

	edgeDiagnoseExample = `
# Diagnose whether the node is normal
keadm debug diagnose node

# Diagnose whether the pod is normal
keadm debug diagnose pod nginx-xxx -n test

# Diagnose node installation conditions
keadm debug diagnose install

# Diagnose node installation conditions and specify the detected ip
keadm debug diagnose install -i 192.168.1.2
`
)

type Diagnose common.DiagnoseObject

// NewDiagnose returns KubeEdge edge debug Diagnose command.
func NewDiagnose() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "diagnose",
		Short:   edgeDiagnoseShortDescription,
		Long:    edgeDiagnoseLongDescription,
		Example: edgeDiagnoseExample,
	}
	for _, v := range common.DiagnoseObjectMap {
		cmd.AddCommand(NewSubDiagnose(Diagnose(v)))
	}
	return cmd
}

func NewSubDiagnose(object Diagnose) *cobra.Command {
	do := NewDiagnoseOptions()
	cmd := &cobra.Command{
		Short: object.Desc,
		Use:   object.Use,
		Run: func(cmd *cobra.Command, args []string) {
			object.ExecuteDiagnose(object.Use, do, args)
		},
	}
	switch object.Use {
	case common.ArgDiagnoseNode:
		cmd.Flags().StringVarP(&do.Config, common.EdgecoreConfig, "c", do.Config,
			fmt.Sprintf("Specify configuration file, default is %s", constants.EdgecoreConfigPath))
	case common.ArgDiagnosePod:
		cmd.Flags().StringVarP(&do.Namespace, "namespace", "n", do.Namespace, "specify namespace")
	case common.ArgDiagnoseInstall:
		cmd.Flags().StringVarP(&do.CheckOptions.DNSIP, "dns-ip", "D", do.CheckOptions.DNSIP, "specify test dns server ip")
		cmd.Flags().StringVarP(&do.CheckOptions.Domain, "domain", "d", do.CheckOptions.Domain, "specify test domain")
		cmd.Flags().StringVarP(&do.CheckOptions.IP, "ip", "i", do.CheckOptions.IP, "specify test ip")
		cmd.Flags().StringVarP(&do.CheckOptions.CloudHubServer, "cloud-hub-server", "s", do.CheckOptions.CloudHubServer, "specify cloudhub server")
	}
	return cmd
}

// NewDiagnoseOptions returns diagnose options
func NewDiagnoseOptions() *common.DiagnoseOptions {
	do := &common.DiagnoseOptions{}
	do.Namespace = "default"
	do.Config = constants.EdgecoreConfigPath
	do.CheckOptions = &common.CheckOptions{
		IP:      "",
		Timeout: 3,
	}
	return do
}

func (da Diagnose) ExecuteDiagnose(use string, ops *common.DiagnoseOptions, args []string) {
	var err error
	switch use {
	case common.ArgDiagnoseNode:
		err = DiagnoseNode(ops)
	case common.ArgDiagnosePod:
		if len(args) == 0 {
			fmt.Println("error: You must specify a pod name")
			return
		}
		// diagnose Pod, first diagnose node
		err = DiagnoseNode(ops)
		if err == nil {
			err = DiagnosePod(ops, args[0])
		}
	case common.ArgDiagnoseInstall:
		err = DiagnoseInstall(ops.CheckOptions)
	}

	if err != nil {
		fmt.Println(err.Error())
		util.PrintFail(use, common.StrDiagnose)
	} else {
		util.PrintSucceed(use, common.StrDiagnose)
	}
}

func DiagnoseNode(ops *common.DiagnoseOptions) error {
	osType := util.GetOSInterface()
	isEdgeRunning, err := osType.IsKubeEdgeProcessRunning(constants.KubeEdgeBinaryName)
	if err != nil {
		return fmt.Errorf("get edgecore status fail")
	}

	if !isEdgeRunning {
		return fmt.Errorf("edgecore is not running")
	}
	fmt.Println("edgecore is running")

	isFileExists := files.FileExists(ops.Config)
	if !isFileExists {
		return fmt.Errorf("edge config is not exists")
	}
	fmt.Printf("edge config is exists: %v\n", ops.Config)

	edgeconfig, err := util.ParseEdgecoreConfig(ops.Config)
	if err != nil {
		return fmt.Errorf("parse edgecore config failed")
	}

	// check datebase
	dataSource := v1alpha2.DataBaseDataSource
	if edgeconfig.DataBase.DataSource != "" {
		dataSource = edgeconfig.DataBase.DataSource
	}
	ops.DBPath = dataSource
	isFileExists = files.FileExists(dataSource)
	if !isFileExists {
		return fmt.Errorf("dataSource is not exists")
	}
	fmt.Printf("dataSource is exists: %v\n", dataSource)

	//CheckNetWork
	if !edgeconfig.Modules.EdgeHub.WebSocket.Enable {
		return fmt.Errorf("edgehub is not enable")
	}

	cloudURL := edgeconfig.Modules.EdgeHub.WebSocket.Server
	err = CheckHTTP("https://" + cloudURL)
	if err != nil {
		return fmt.Errorf("cloudcore websocket connection failed")
	}
	fmt.Printf("cloudcore websocket connection success")

	return nil
}

func DiagnosePod(ops *common.DiagnoseOptions, podName string) error {
	var ready bool
	if ops.DBPath == "" {
		ops.DBPath = v1alpha2.DataBaseDataSource
	}
	err := InitDB(v1alpha2.DataBaseDriverName, v1alpha2.DataBaseAliasName, ops.DBPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %v ", err)
	}
	fmt.Printf("Database %s is exist \n", v1alpha2.DataBaseDataSource)
	podStatus, err := QueryPodFromDatabase(ops.Namespace, podName)
	if err != nil {
		return err
	}

	fmt.Printf("pod %v phase is %v \n", podName, podStatus.Phase)
	if podStatus.Phase != "Running" {
		ready = false
	}

	conditions := podStatus.Conditions
	containerConditions := podStatus.ContainerStatuses

	// check conditions
	for _, v := range conditions {
		if v.Type == "Ready" && v.Status == "True" {
			ready = true
		}
		if v.Status != "True" {
			fmt.Printf("conditions is not true, type: %v ,message: %v ,reason: %v \n",
				v.Type, v.Message, v.Reason)
		}
	}
	// check containerConditions
	for _, v := range containerConditions {
		if !v.Ready {
			if v.State.Waiting != nil {
				fmt.Printf("containerConditions %v Waiting, message: %v, reason: %v, RestartCount: %v \n", v.Name,
					v.State.Waiting.Message, v.State.Waiting.Reason, v.RestartCount)
			} else if v.State.Terminated != nil {
				fmt.Printf("containerConditions %v Terminated, message: %v, reason: %v, RestartCount: %v \n", v.Name,
					v.State.Terminated.Message, v.State.Terminated.Reason, v.RestartCount)
			} else {
				fmt.Printf("containerConditions %v is not ready\n", v.Name)
			}
		} else {
			fmt.Printf("containerConditions %v is ready\n", v.Name)
		}
	}
	if ready {
		fmt.Printf("Pod %s is Ready", podName)
	} else {
		return fmt.Errorf("pod %s is not Ready", podName)
	}

	return nil
}

func QueryPodFromDatabase(resNamePaces string, podName string) (*v1.PodStatus, error) {
	conditionsPod := fmt.Sprintf("%v/pod/%v",
		resNamePaces,
		podName)
	resultPod, err := dao.QueryMeta("key", conditionsPod)
	if err != nil {
		return nil, fmt.Errorf("read database fail: %s", err.Error())
	}
	if len(*resultPod) == 0 {
		return nil, fmt.Errorf("not find %v in datebase", conditionsPod)
	}
	fmt.Printf("Pod %s is exist \n", podName)

	conditionsStatus := fmt.Sprintf("%v/podstatus/%v",
		resNamePaces,
		podName)
	resultStatus, err := dao.QueryMeta("key", conditionsStatus)
	if err != nil {
		return nil, fmt.Errorf("read database fail: %s", err.Error())
	}
	if len(*resultStatus) == 0 {
		fmt.Printf("not find %v in datebase\n", conditionsStatus)
		r := *resultPod
		pod := &v1.Pod{}
		err = json.Unmarshal([]byte(r[0]), pod)
		if err != nil {
			return &pod.Status, err
		}
		return &pod.Status, nil
	}
	fmt.Printf("PodStatus %s is exist \n", podName)

	r := *resultStatus
	podStatus := &types.PodStatusRequest{}
	err = json.Unmarshal([]byte(r[0]), podStatus)
	if err != nil {
		return &podStatus.Status, err
	}
	return &podStatus.Status, nil
}

func DiagnoseInstall(ob *common.CheckOptions) error {
	if err := CheckCPU(); err != nil {
		return err
	}
	if err := CheckMemory(); err != nil {
		return err
	}
	if err := CheckDisk(); err != nil {
		return err
	}
	if ob.Domain != "" {
		if err := CheckDNSSpecify(ob.Domain, ob.DNSIP); err != nil {
			return err
		}
	}
	if err := CheckNetWork(ob.IP, ob.Timeout, ob.CloudHubServer,
		ob.EdgecoreServer, ob.Config); err != nil {
		return err
	}
	if err := CheckPid(); err != nil {
		return err
	}
	return nil
}
