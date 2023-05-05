package main

import (
	"context"
	"fmt"
	"os"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var file = "../../.kube/workload-config"

func main() {
	b, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(b)
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}
	pods, err := clientset.CoreV1().Pods("default").List(context.TODO(), v1.ListOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(pods)
}
