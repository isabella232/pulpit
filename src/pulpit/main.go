// Note: the example only works with the code within the same release/branch.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	apiv1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	//
	// Uncomment to load all auth plugins
	// _ "k8s.io/client-go/plugin/pkg/client/auth"
	//
	// Or uncomment to load specific auth plugins
	// _ "k8s.io/client-go/plugin/pkg/client/auth/azure"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	// _ "k8s.io/client-go/plugin/pkg/client/auth/openstack"
)

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// *kubeconfig is empty string if running inside a cluster
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	namespace := "pulsar"

	// clientset.AppsV1().RESTClient().Get()

	deploymentsClient := clientset.AppsV1().Deployments(namespace)
	// deploymentsClient := clientset.AppsV1().Deployments(apiv1.NamespaceDefault)

	// List Deployments
	fmt.Printf("Listing deployments in namespace %q:\n", apiv1.NamespaceDefault)
	list, err := deploymentsClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for _, d := range list.Items {
		fmt.Printf(" * %s (%d replicas)\n", d.Name, *d.Spec.Replicas)
	}

	// list STS
	stsClient := clientset.AppsV1().StatefulSets(namespace)
	list2, err := stsClient.List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err)
	}
	for _, d := range list2.Items {
		fmt.Printf(" sts * %s (%d replicas)\n", d.Name, *d.Spec.Replicas)
	}

	// list PVC
	pvcs, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	for _, pvc := range pvcs.Items {
		quant := pvc.Spec.Resources.Requests[v1.ResourceStorage]
		fmt.Printf(
			"%-32s%-8s%-8s\n",
			pvc.Name,
			string(pvc.Status.Phase),
			quant.String())
	}

	//default namespace is v1.NamespaceDefault
	watchlist := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "pods", namespace, fields.Everything())
	_, controller := cache.NewInformer(
		watchlist,
		&v1.Service{},
		time.Second*0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				fmt.Printf("service added: %v \n", obj)
			},
			DeleteFunc: func(obj interface{}) {
				fmt.Printf("service deleted: %v \n", obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				fmt.Printf("service changed \n")
			},
		},
	)
	stop := make(chan struct{})
	go controller.Run(stop)
	for {
		time.Sleep(time.Second)
	}

	// loop through events
	var maxClaims string
	flag.StringVar(&maxClaims, "max-claims", "200Gi",
		"Maximum total claims to watch")
	var totalClaimedQuant resource.Quantity
	maxClaimedQuant := resource.MustParse(maxClaims)

	// watch future changes to PVCs
	watcher, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	ch := watcher.ResultChan()

	fmt.Printf("--- PVC Watch (max claims %v) ----\n", maxClaimedQuant.String())
	for event := range ch {
		pvc, ok := event.Object.(*v1.PersistentVolumeClaim)
		if !ok {
			log.Fatal("unexpected type")
		}
		quant := pvc.Spec.Resources.Requests[v1.ResourceStorage]

		switch event.Type {
		case watch.Added:
			totalClaimedQuant.Add(quant)
			log.Printf("PVC %s added, claim size %s\n", pvc.Name, quant.String())

			// is claim overage?
			if totalClaimedQuant.Cmp(maxClaimedQuant) == 1 {
				log.Printf("\nClaim overage reached: max %s at %s",
					maxClaimedQuant.String(),
					totalClaimedQuant.String(),
				)
				// trigger action
				log.Println("*** Taking action ***\n")
			}

		case watch.Modified:
			//log.Printf("Pod %s modified\n", pod.GetName())
		case watch.Deleted:
			quant := pvc.Spec.Resources.Requests[v1.ResourceStorage]
			totalClaimedQuant.Sub(quant)
			log.Printf("PVC %s removed, size %s\n", pvc.Name, quant.String())

			if totalClaimedQuant.Cmp(maxClaimedQuant) <= 0 {
				log.Printf("Claim usage normal: max %s at %s",
					maxClaimedQuant.String(),
					totalClaimedQuant.String(),
				)
				// trigger action
				log.Println("*** Taking action ***")
			}
		case watch.Error:
			//log.Printf("watcher error encountered\n", pod.GetName())
		}

		log.Printf("\nAt %3.1f%% claim capcity (%s/%s)\n",
			float64(totalClaimedQuant.Value())/float64(maxClaimedQuant.Value())*100,
			totalClaimedQuant.String(),
			maxClaimedQuant.String(),
		)
	}

}

func prompt() {
	fmt.Printf("-> Press Return key to continue.")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		break
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	fmt.Println()
}

func int32Ptr(i int32) *int32 { return &i }
