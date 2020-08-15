package namespace

import (
	"fmt"
	"os"

	"github.com/jenkins-x/jx-helpers/pkg/cobras/helper"
	"github.com/jenkins-x/jx-helpers/pkg/cobras/templates"
	"github.com/jenkins-x/jx-helpers/pkg/input"
	"github.com/jenkins-x/jx-helpers/pkg/input/survey"
	"github.com/jenkins-x/jx-kube-client/pkg/kubeclient"
	"github.com/jenkins-x/jx-logging/pkg/log"
	"github.com/spf13/cobra"

	"github.com/pkg/errors"

	"github.com/jenkins-x/jx-helpers/pkg/kube"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"sort"

	"github.com/jenkins-x/jx-helpers/pkg/termcolor"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd/api"
)

type Options struct {
	KubeClient kubernetes.Interface
	Input      input.Interface
	Args       []string
	Create     bool
	QuiteMode  bool
	BatchMode  bool
}

var (
	cmdLong = templates.LongDesc(`
		Displays or changes the current namespace.`)
	cmdExample = templates.Examples(`
		# view the current namespace
		jx --batch-mode ns

		# interactively select the namespace to switch to
		jx ns

		# change the current namespace to 'cheese'
		jx ns cheese

		# change the current namespace to 'brie' creating it if necessary
		jx ns --create brie`)
)

func NewCmdNamespace() (*cobra.Command, *Options) {
	options := &Options{}
	cmd := &cobra.Command{
		Use:     "namespace",
		Aliases: []string{"ns"},
		Short:   "View or change the current namespace context in the current Kubernetes cluster",
		Long:    cmdLong,
		Example: cmdExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Args = args
			err := options.Run()
			helper.CheckErr(err)
		},
	}

	cmd.Flags().BoolVarP(&options.Create, "create", "c", false, "Creates the specified namespace if it does not exist")
	cmd.Flags().BoolVarP(&options.BatchMode, "batch-mode", "b", false, "Enables batch mode")
	cmd.Flags().BoolVarP(&options.QuiteMode, "quiet", "q", false, "Do not fail if the namespace does not exist")
	return cmd, options
}

func (o *Options) Run() error {
	var err error
	currentNS := ""
	o.KubeClient, currentNS, err = kube.LazyCreateKubeClientAndNamespace(o.KubeClient, "")
	if err != nil {
		return errors.Wrap(err, "creating kubernetes client")
	}
	client := o.KubeClient

	f := kubeclient.NewFactory()
	config, err := f.CreateKubeConfig()
	if err != nil {
		return errors.Wrap(err, "creating kubernetes configuration")
	}
	cfg, pathOptions, err := kubeclient.LoadConfig()
	if err != nil {
		return errors.Wrap(err, "loading Kubernetes configuration")
	}
	ns := namespace(o)
	if ns == "" && !o.BatchMode {
		ns, err = pickNamespace(o, client, currentNS)
		if err != nil {
			return err
		}
	}

	info := termcolor.ColorInfo
	if ns != "" && ns != currentNS {
		ctx, err := changeNamespace(client, cfg, pathOptions, ns, o.Create, o.QuiteMode)
		if err != nil {
			return err
		}
		if ctx == nil {
			log.Logger().Infof("No kube context - probably in a unit test or pod?\n")
		} else {
			log.Logger().Infof("Now using namespace '%s' on server '%s'.\n", info(ctx.Namespace), info(kube.Server(cfg, ctx)))
		}
	} else {
		if currentNS != "" {
			ns = currentNS
		}
		server := kube.CurrentServer(cfg)
		if config == nil {
			log.Logger().Infof("Using namespace '%s' on server '%s'. No context - probably a unit test or pod?\n", info(ns), info(server))

		} else {
			log.Logger().Infof("Using namespace '%s' from context named '%s' on server '%s'.\n", info(ns), info(cfg.CurrentContext), info(server))
		}
	}
	return nil
}

func namespace(o *Options) string {
	ns := ""
	args := o.Args
	if len(args) > 0 {
		ns = args[0]
	}
	return ns
}

func changeNamespace(client kubernetes.Interface, config *api.Config, pathOptions clientcmd.ConfigAccess, ns string, create, quietMode bool) (*api.Context, error) {
	_, err := client.CoreV1().Namespaces().Get(ns, metav1.GetOptions{})
	if err != nil {
		switch err.(type) {
		case *apierrors.StatusError:
			err = handleStatusError(err, client, ns, create, quietMode)
			if err != nil {
				return nil, err
			}
		default:
			return nil, errors.Wrapf(err, "getting namespace %q", ns)
		}
	}
	newConfig := *config
	ctx := kube.CurrentContext(config)
	if ctx == nil {
		log.Logger().Warnf("there is no context defined in your Kubernetes configuration - we may be inside a test case or pod?\n")
		return ctx, nil
	}
	if ctx.Namespace == ns {
		return ctx, nil
	}
	ctx.Namespace = ns
	err = clientcmd.ModifyConfig(pathOptions, newConfig, false)
	if err != nil {
		return nil, fmt.Errorf("failed to update the kube config %s", err)
	}
	return ctx, nil
}

func handleStatusError(err error, client kubernetes.Interface, ns string, create, quietMode bool) error {
	statusErr, _ := err.(*apierrors.StatusError)
	if statusErr.Status().Reason == metav1.StatusReasonNotFound {
		if quietMode {
			log.Logger().Infof("namespace %s does not exist yet", ns)
			os.Exit(0)
			return nil
		}
		if create {
			err = createNamespace(client, ns)
			if err != nil {
				return err
			}
		}
	} else {
		return err
	}
	return nil
}

func createNamespace(client kubernetes.Interface, ns string) error {
	namespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	}
	_, err := client.CoreV1().Namespaces().Create(&namespace)
	if err != nil {
		return errors.Wrapf(err, "unable to create namespace %s", ns)
	}
	return nil
}

func pickNamespace(o *Options, client kubernetes.Interface, defaultNamespace string) (string, error) {
	names, err := getNamespaceNames(client)
	if err != nil {
		return "", errors.Wrap(err, "retrieving namespace the names of the namespaces")
	}

	selectedNamespace, err := pick(o, names, defaultNamespace)
	if err != nil {
		return "", errors.Wrap(err, "picking the namespace")
	}
	return selectedNamespace, nil
}

// getNamespaceNames returns the sorted list of environment names
func getNamespaceNames(client kubernetes.Interface) ([]string, error) {
	var names []string
	list, err := client.CoreV1().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		return names, fmt.Errorf("loading namespaces %s", err)
	}
	for k := range list.Items {
		names = append(names, list.Items[k].Name)
	}
	sort.Strings(names)
	return names, nil
}

func pick(o *Options, names []string, defaultNamespace string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	if len(names) == 1 {
		return names[0], nil
	}
	if o.Input == nil {
		o.Input = survey.NewInput()
	}
	name, err := o.Input.PickNameWithDefault(names, "Change namespace:", defaultNamespace, "pick the kubernetes namespace for the current kubernetes cluster")
	return name, err
}
