package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/urfave/cli"
)

var (
	// Build revision is set at compile time.
	rev string
)

const (
	gcloudCmd  = "gcloud"
	kubectlCmd = "kubectl"

	keyPath = "/tmp/gcloud.json"
	nsPath  = "/tmp/namespace.json"
)

var nsTemplate = `
---
apiVersion: v1
kind: Namespace
metadata:
  name: %s
`

func main() {
	err := wrapMain()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func wrapMain() error {
	if rev == "" {
		rev = "[unknown]"
	}

	fmt.Printf("Drone GKE Plugin built from %s\n", rev)

	app := cli.NewApp()
	app.Name = "gke plugin"
	app.Usage = "gke plugin"
	app.Action = run
	app.Version = fmt.Sprintf("1.0.0-%s", rev)
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:   "dry-run",
			Usage:  "do not apply the Kubernetes manifests to the API server",
			EnvVar: "PLUGIN_DRY_RUN",
		},
		cli.BoolFlag{
			Name:   "verbose",
			Usage:  "dump available vars and the generated Kubernetes manifest, keeping secrets hidden",
			EnvVar: "PLUGIN_VERBOSE",
		},
		cli.StringFlag{
			Name:   "token",
			Usage:  "service account's JSON credentials",
			EnvVar: "TOKEN",
		},
		cli.StringFlag{
			Name:   "project",
			Usage:  "GCP project name",
			EnvVar: "PLUGIN_PROJECT",
		},
		cli.StringFlag{
			Name:   "zone",
			Usage:  "zone of the container cluster",
			EnvVar: "PLUGIN_ZONE",
		},
		cli.StringFlag{
			Name:   "cluster",
			Usage:  "name of the container cluster",
			EnvVar: "PLUGIN_CLUSTER",
		},
		cli.StringFlag{
			Name:   "namespace",
			Usage:  "Kubernetes namespace to operate in",
			EnvVar: "PLUGIN_NAMESPACE",
		},
		cli.StringFlag{
			Name:   "kube-template",
			Usage:  "optional - template for Kubernetes resources, e.g. deployments",
			EnvVar: "PLUGIN_TEMPLATE",
			Value:  ".kube.yml",
		},
		cli.StringFlag{
			Name:   "secret-template",
			Usage:  "optional - template for Kubernetes Secret resources",
			EnvVar: "PLUGIN_SECRET_TEMPLATE",
			Value:  ".kube.sec.yml",
		},
		cli.StringFlag{
			Name:   "vars",
			Usage:  "variables to use while templating manifests",
			EnvVar: "PLUGIN_VARS",
		},
		cli.StringFlag{
			Name:   "drone-build-number",
			Usage:  "Drone build number",
			EnvVar: "DRONE_BUILD_NUMBER",
		},
		cli.StringFlag{
			Name:   "drone-commit",
			Usage:  "Git commit hash",
			EnvVar: "DRONE_COMMIT",
		},
		cli.StringFlag{
			Name:   "drone-branch",
			Usage:  "Git branch",
			EnvVar: "DRONE_BRANCH",
		},
		cli.StringFlag{
			Name:   "drone-tag",
			Usage:  "Git tag",
			EnvVar: "DRONE_TAG",
		},
	}

	if err := app.Run(os.Args); err != nil {
		return err
	}

	return nil
}

func run(c *cli.Context) error {
	// Check required params.

	// Trim whitespace, to forgive the vagaries of YAML parsing.
	token := strings.TrimSpace(c.String("token"))
	if token == "" {
		return fmt.Errorf("Missing required param: token")
	}

	// Use project if explicitly stated, otherwise infer from the service account token.
	project := c.String("project")
	if project == "" {
		project = getProjectFromToken(token)
		if project == "" {
			return fmt.Errorf("Missing required param: project")
		}
	}

	if c.String("zone") == "" {
		return fmt.Errorf("Missing required param: zone")
	}

	// Enforce default values.
	kubeTemplate := c.String("kube-template")
	if kubeTemplate == "" {
		kubeTemplate = ".kube.yml"
	}

	secretTemplate := c.String("secret-template")
	if secretTemplate == "" {
		secretTemplate = ".kube.sec.yml"
	}

	// Parse variables.
	vars := make(map[string]interface{})
	varsJSON := c.String("vars")
	if varsJSON != "" {
		if err := json.Unmarshal([]byte(varsJSON), &vars); err != nil {
			return fmt.Errorf("Error parsing vars: %s\n", err)
		}
	}

	// Parse secrets.
	secrets := make(map[string]string)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SECRET_") {
			continue
		}

		// Only split up to 2 parts.
		pair := strings.SplitN(e, "=", 2)

		// Check that key and value both exist.
		if len(pair) != 2 {
			return fmt.Errorf("Error: secret key and value mismatch, expected 2, got %d", len(pair))
		}

		k := pair[0]
		v := pair[1]

		if _, ok := secrets[k]; ok {
			return fmt.Errorf("Error: secret var %q shadows existing secret\n", k)
		}

		if v == "" {
			return fmt.Errorf("Error: secret var %q is an empty string\n", k)
		}

		secrets[k] = v

		os.Unsetenv(k)
	}

	// Write credentials to tmp file to be picked up by the 'gcloud' command.
	// This is inside the ephemeral plugin container, not on the host.
	err := ioutil.WriteFile(keyPath, []byte(token), 0600)
	if err != nil {
		return fmt.Errorf("Error writing token file: %s\n", err)
	}

	// Warn if the keyfile can't be deleted, but don't abort.
	// We're almost certainly running inside an ephemeral container, so the file will be discarded when we're finished anyway.
	defer func() {
		err := os.Remove(keyPath)
		if err != nil {
			log("Warning: error removing token file: %s\n", err)
		}
	}()

	// Set up the execution environment.
	e := os.Environ()
	e = append(e, fmt.Sprintf("GOOGLE_APPLICATION_CREDENTIALS=%s", keyPath))

	runner := NewEnviron("", e, os.Stdout, os.Stderr)

	err = runner.Run(gcloudCmd, "auth", "activate-service-account", "--key-file", keyPath)
	if err != nil {
		return fmt.Errorf("Error: %s\n", err)
	}

	err = runner.Run(gcloudCmd, "container", "clusters", "get-credentials", c.String("cluster"), "--project", project, "--zone", c.String("zone"))
	if err != nil {
		return fmt.Errorf("Error: %s\n", err)
	}

	data := map[string]interface{}{
		"BUILD_NUMBER": c.String("drone-build-number"),
		"COMMIT":       c.String("drone-commit"),
		"BRANCH":       c.String("drone-branch"),
		"TAG":          c.String("drone-tag"),

		// Misc useful stuff.
		// Note that secrets (including the GCP token) are excluded
		"project":   project,
		"zone":      c.String("zone"),
		"cluster":   c.String("cluster"),
		"namespace": c.String("namespace"),
	}

	secretsAndData := map[string]interface{}{}

	// Add variables to data used for rendering both templates.
	for k, v := range vars {
		// Don't allow vars to be overridden.
		// We do this to ensure that the built-in template vars (above) can be relied upon.
		if _, ok := data[k]; ok {
			return fmt.Errorf("Error: var %q shadows existing var\n", k)
		}

		data[k] = v
		secretsAndData[k] = v
	}

	// Add secrets to data used for rendering the Secret template.
	for k, v := range secrets {
		// Don't allow vars to be overridden.
		// We do this to ensure that the built-in template vars (above) can be relied upon.
		if _, ok := secretsAndData[k]; ok {
			return fmt.Errorf("Error: secret var %q shadows existing var\n", k)
		}

		secretsAndData[k] = v
	}

	if c.Bool("verbose") {
		dumpData(os.Stdout, "VARIABLES AVAILABLE FOR TEMPLATES", data)
	}

	// mapping is a map of the template filename to the data it uses for rendering.
	mapping := map[string]map[string]interface{}{
		kubeTemplate:   data,
		secretTemplate: secretsAndData,
	}

	outPaths := make(map[string]string)
	pathArg := []string{}

	for t, content := range mapping {
		if t == "" {
			continue
		}

		// Ensure the required template file exists.
		_, err := os.Stat(t)
		if os.IsNotExist(err) {
			if t == kubeTemplate {
				return fmt.Errorf("Error finding template: %s\n", err)
			}

			log("Warning: skipping optional template %s because it was not found\n", t)
			continue
		}

		// Create the output file.
		outPaths[t] = fmt.Sprintf("/tmp/%s", t)
		f, err := os.Create(outPaths[t])
		if err != nil {
			return fmt.Errorf("Error creating deployment file: %s\n", err)
		}

		// Read the template.
		blob, err := ioutil.ReadFile(t)
		if err != nil {
			return fmt.Errorf("Error reading template: %s\n", err)
		}

		// Parse the template.
		tmpl := template.Must(template.New(t).Funcs(sprig.TxtFuncMap()).Option("missingkey=error").Parse(string(blob)))

		// Generate the manifest.
		err = tmpl.Execute(f, content)
		if err != nil {
			return fmt.Errorf("Error rendering deployment manifest from template: %s\n", err)
		}

		f.Close()

		// Add the manifest filepath to the list of manifests to apply.
		pathArg = append(pathArg, outPaths[t])
	}

	if c.Bool("verbose") {
		dumpFile(os.Stdout, "RENDERED MANIFEST (Secret Manifest Omitted)", outPaths[kubeTemplate])
	}

	// Print kubectl version.
	err = runner.Run(kubectlCmd, "version")
	if err != nil {
		return fmt.Errorf("Error: %s\n", err)
	}

	namespace := c.String("namespace")

	if namespace != "" {
		// Set the execution namespace.
		log("Configuring kubectl to the %s namespace\n", namespace)

		context := strings.Join([]string{"gke", project, c.String("zone"), c.String("cluster")}, "_")
		err = runner.Run(kubectlCmd, "config", "set-context", context, "--namespace", namespace)
		if err != nil {
			return fmt.Errorf("Error: %s\n", err)
		}

		// Write the namespace manifest to a tmp file for application.
		resource := fmt.Sprintf(nsTemplate, namespace)

		err := ioutil.WriteFile(nsPath, []byte(resource), 0600)
		if err != nil {
			return fmt.Errorf("Error writing namespace resource file: %s\n", err)
		}

		// Ensure the namespace exists, without errors (unlike `kubectl create namespace`).
		log("Ensuring the %s namespace exists\n", namespace)

		nsArgs := applyArgs(c.Bool("dry-run"), nsPath)
		err = runner.Run(kubectlCmd, nsArgs...)
		if err != nil {
			return fmt.Errorf("Error: %s\n", err)
		}
	}

	manifests := strings.Join(pathArg, ",")

	// If it is not a dry run, do a dry run first to validate Kubernetes manifests.
	log("Validating Kubernetes manifests with a dry-run\n")

	if !c.Bool("dry-run") {
		args := applyArgs(true, manifests)
		err = runner.Run(kubectlCmd, args...)
		if err != nil {
			return fmt.Errorf("Error: %s\n", err)
		}

		log("Applying Kubernetes manifests to the cluster\n")
	}

	// Actually apply Kubernetes manifests.

	args := applyArgs(c.Bool("dry-run"), manifests)
	err = runner.Run(kubectlCmd, args...)
	if err != nil {
		return fmt.Errorf("Error: %s\n", err)
	}

	return nil
}

type token struct {
	ProjectID string `json:"project_id"`
}

func getProjectFromToken(j string) string {
	t := token{}
	err := json.Unmarshal([]byte(j), &t)
	if err != nil {
		return ""
	}
	return t.ProjectID
}

func applyArgs(dryrun bool, file string) []string {
	args := []string{
		"apply",
		"--record",
	}

	if dryrun {
		args = append(args, "--dry-run")
	}

	args = append(args, "--filename")
	args = append(args, file)

	return args
}

func log(format string, a ...interface{}) {
	fmt.Printf("\n"+format, a...)
}
