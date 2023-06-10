package rules

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/ghodss/yaml"
	kubeeyev1alpha2 "github.com/kubesphere/kubeeye/apis/kubeeye/v1alpha2"
	"github.com/kubesphere/kubeeye/clients/clientset/versioned"
	"github.com/kubesphere/kubeeye/constant"
	"github.com/kubesphere/kubeeye/pkg/utils"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	kubeErr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ruleFiles
var defaultRegoRules embed.FS

func GetDefaultRegofile(path string) []map[string][]byte {
	var regoRules []map[string][]byte
	files, err := defaultRegoRules.ReadDir(path)
	if err != nil {
		fmt.Printf("Failed to get Default Rego rule files.\n")
	}
	for _, file := range files {
		rule, _ := defaultRegoRules.ReadFile(path + "/" + file.Name())
		regoRule := map[string][]byte{"name": []byte(file.Name()), "rule": rule}
		regoRules = append(regoRules, regoRule)
	}
	return regoRules
}

func RegoToRuleYaml(path string) {
	regofile := GetDefaultRegofile(path)
	var inspectRules []kubeeyev1alpha2.InspectRule

	for _, m := range regofile {
		var ruleItems []kubeeyev1alpha2.OpaRule
		var inspectRule kubeeyev1alpha2.InspectRule
		opaRule := kubeeyev1alpha2.OpaRule{}
		var space string
		opaRule.Name = strings.Replace(string(m["name"]), ".rego", "", -1)
		var rule = string(m["rule"])
		opaRule.Rule = &rule
		scanner := bufio.NewScanner(bytes.NewReader(m["rule"]))
		if scanner.Scan() {
			space = strings.TrimSpace(strings.Replace(scanner.Text(), "package", "", -1))
		}
		opaRule.Module = space
		for i := range inspectRules {
			if space == inspectRules[i].Labels[constant.LabelRuleTag] {
				inspectRule = inspectRules[i]
				inspectRules = append(inspectRules[:i], inspectRules[i+1:]...)
				break
			}
		}

		ruleItems = append(ruleItems, opaRule)

		inspectRule.Labels = map[string]string{
			"app.kubernetes.io/name":       "inspectrules",
			"app.kubernetes.io/instance":   "inspectrules-sample",
			"app.kubernetes.io/part-of":    "kubeeye",
			"app.kubernetes.io/managed-by": "kustomize",
			"app.kubernetes.io/created-by": "kubeeye",
			constant.LabelRuleTag:          space,
		}
		if inspectRule.Spec.Opas != nil {
			ruleItems = append(ruleItems, inspectRule.Spec.Opas...)
		}

		inspectRule.Spec.Opas = ruleItems
		inspectRule.Name = fmt.Sprintf("%s-%s", "kubeeye-inspectrules", strconv.Itoa(int(time.Now().Unix())))
		inspectRule.Namespace = "kubeeye-system"
		inspectRule.APIVersion = "kubeeye.kubesphere.io/v1alpha2"
		inspectRule.Kind = "InspectRule"
		inspectRules = append(inspectRules, inspectRule)
	}

	for i := range inspectRules {

		data, err := yaml.Marshal(&inspectRules[i])
		if err != nil {
			panic(err)
		}
		filename := fmt.Sprintf("./ruleFiles/kubeeye_v1alpha2_inspectrules%d_%d.yaml", i, time.Now().Unix())
		err = ioutil.WriteFile(filename, data, 0644)
		if err != nil {
			panic(err)
		}
	}
	fmt.Println("YAML file written successfully")
}

func GetRules(ctx context.Context, task types.NamespacedName, client versioned.Interface) map[string][]byte {

	_, err := client.KubeeyeV1alpha2().InspectTasks(task.Namespace).Get(ctx, task.Name, metav1.GetOptions{})
	if err != nil {
		if kubeErr.IsNotFound(err) {
			fmt.Printf("rego ruleFiles not found .\n")
			return nil
		}
		fmt.Printf("Failed to Get rego ruleFiles.\n")
		return nil
	}
	return nil
}

// MergeRegoRules fun-out merge rego ruleFiles
func MergeRegoRules(ctx context.Context, channels ...[]string) <-chan string {
	res := make(chan string)
	var wg sync.WaitGroup
	wg.Add(len(channels))

	mergeRegoRuls := func(ctx context.Context, ch []string) {
		defer wg.Done()
		for _, c := range ch {
			res <- c
		}
	}

	for _, c := range channels {
		go mergeRegoRuls(ctx, c)
	}

	go func() {
		wg.Wait()
		defer close(res)
	}()
	return res
}

func MergeRule(rules []kubeeyev1alpha2.InspectRule) (ruleSpec kubeeyev1alpha2.InspectRuleSpec) {
	for _, rule := range rules {
		if rule.Spec.Opas != nil && len(rule.Spec.Opas) > 0 {
			ruleSpec.Opas = append(ruleSpec.Opas, rule.Spec.Opas...)
		}
		if rule.Spec.Prometheus != nil {
			for index := range rule.Spec.Prometheus {
				if "" != rule.Spec.PrometheusEndpoint && len(rule.Spec.PrometheusEndpoint) > 0 {
					rule.Spec.Prometheus[index].Endpoint = rule.Spec.PrometheusEndpoint
				}
				ruleSpec.Prometheus = append(ruleSpec.Prometheus, rule.Spec.Prometheus[index])
			}
		}
		if rule.Spec.FileChange != nil && len(rule.Spec.FileChange) > 0 {
			ruleSpec.FileChange = append(ruleSpec.FileChange, rule.Spec.FileChange...)
		}
		if rule.Spec.Sysctl != nil && len(rule.Spec.Sysctl) > 0 {
			ruleSpec.Sysctl = append(ruleSpec.Sysctl, rule.Spec.Sysctl...)
		}
		if rule.Spec.Systemd != nil && len(rule.Spec.Systemd) > 0 {
			ruleSpec.Systemd = append(ruleSpec.Systemd, rule.Spec.Systemd...)
		}
	}
	return ruleSpec
}

func AllocationOpa(rule []kubeeyev1alpha2.OpaRule, taskName string) *kubeeyev1alpha2.JobRule {
	if rule == nil {
		return nil
	}

	jobRule := &kubeeyev1alpha2.JobRule{
		JobName:  fmt.Sprintf("%s-%s", taskName, constant.Opa),
		RuleType: constant.Opa,
	}

	opa, err := json.Marshal(rule)
	if err != nil {
		klog.Errorf("Failed to marshal  opa rule. err:%s", err)
		return nil
	}
	jobRule.RunRule = opa
	return jobRule
}

func AllocationPrometheus(rule []kubeeyev1alpha2.PrometheusRule, taskName string) *kubeeyev1alpha2.JobRule {
	if rule == nil {
		return nil
	}

	jobRule := &kubeeyev1alpha2.JobRule{
		JobName:  fmt.Sprintf("%s-%s", taskName, constant.Prometheus),
		RuleType: constant.Prometheus,
	}

	prometheus, err := json.Marshal(rule)
	if err != nil {
		klog.Errorf("Failed to marshal  prometheus rule. err:%s", err)
		return nil
	}
	jobRule.RunRule = prometheus
	return jobRule
}
func AllocationFileChange(rule []kubeeyev1alpha2.FileChangeRule, taskName string, allNode corev1.NodeList) []kubeeyev1alpha2.JobRule {
	if rule == nil {
		return nil
	}
	nodeData, filterData := utils.ArrayFilter(rule, func(v kubeeyev1alpha2.FileChangeRule) bool {
		return v.NodeName != nil || v.NodeSelector != nil
	})
	var jobRules []kubeeyev1alpha2.JobRule
	nodeNameMergeMap := mergeNodeRule(nodeData)

	for k, v := range nodeNameMergeMap {
		jobRule := kubeeyev1alpha2.JobRule{
			JobName:  fmt.Sprintf("%s-%s-%s-%s", taskName, constant.FileChange, k, v[0].Name),
			RuleType: constant.FileChange,
		}
		fileChange, err := json.Marshal(v)
		if err != nil {
			klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
			return nil
		}
		jobRule.RunRule = fileChange
		jobRules = append(jobRules, jobRule)
	}

	if filterData != nil && len(filterData) > 0 {
		for _, item := range allNode.Items {
			jobRule := kubeeyev1alpha2.JobRule{
				JobName:  fmt.Sprintf("%s-%s-%s-%s", taskName, constant.FileChange, item.Name, filterData[0].Name),
				RuleType: constant.FileChange,
			}
			fileChange, err := json.Marshal(filterData)
			if err != nil {
				klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
				return nil
			}
			jobRule.RunRule = fileChange
			jobRules = append(jobRules, jobRule)
		}
	}

	return jobRules
}

func AllocationSys(rule []kubeeyev1alpha2.SysRule, taskName string, allNode corev1.NodeList, ctlOrTem string) []kubeeyev1alpha2.JobRule {
	if rule == nil {
		return nil
	}
	nodeData, filterData := utils.ArrayFilter(rule, func(v kubeeyev1alpha2.SysRule) bool {
		return v.NodeName != nil || v.NodeSelector != nil
	})
	var jobRules []kubeeyev1alpha2.JobRule
	nodeNameMergeMap := mergeSysRule(nodeData)

	for k, v := range nodeNameMergeMap {
		jobRule := kubeeyev1alpha2.JobRule{
			JobName:  fmt.Sprintf("%s-%s-%s-%s", taskName, ctlOrTem, k, v[0].Name),
			RuleType: ctlOrTem,
		}
		fileChange, err := json.Marshal(v)
		if err != nil {
			klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
			return nil
		}
		jobRule.RunRule = fileChange
		jobRules = append(jobRules, jobRule)
	}

	if filterData != nil && len(filterData) > 0 {
		for _, item := range allNode.Items {
			jobRule := kubeeyev1alpha2.JobRule{
				JobName:  fmt.Sprintf("%s-%s-%s-%s", taskName, ctlOrTem, item.Name, filterData[0].Name),
				RuleType: ctlOrTem,
			}
			fileChange, err := json.Marshal(filterData)
			if err != nil {
				klog.Errorf("Failed to marshal  fileChange rule. err:%s", err)
				return nil
			}
			jobRule.RunRule = fileChange
			jobRules = append(jobRules, jobRule)
		}
	}

	return jobRules
}

func mergeSysRule(rule []kubeeyev1alpha2.SysRule) map[string][]kubeeyev1alpha2.SysRule {
	var mergeMap = make(map[string][]kubeeyev1alpha2.SysRule)

	for _, changeRule := range rule {
		if changeRule.NodeName != nil {
			mergeMap[*changeRule.NodeName] = append(mergeMap[*changeRule.NodeName], changeRule)
		} else if changeRule.NodeSelector != nil {
			formatLabels := labels.FormatLabels(changeRule.NodeSelector)
			mergeMap[formatLabels] = append(mergeMap[formatLabels], changeRule)
		}
	}

	return mergeMap
}

func mergeNodeRule(rule []kubeeyev1alpha2.FileChangeRule) map[string][]kubeeyev1alpha2.FileChangeRule {
	var mergeMap = make(map[string][]kubeeyev1alpha2.FileChangeRule)

	for _, changeRule := range rule {
		if changeRule.NodeName != nil {
			mergeMap[*changeRule.NodeName] = append(mergeMap[*changeRule.NodeName], changeRule)
		} else if changeRule.NodeSelector != nil {
			formatLabels := labels.FormatLabels(changeRule.NodeSelector)
			mergeMap[formatLabels] = append(mergeMap[formatLabels], changeRule)
		}
	}

	return mergeMap
}
