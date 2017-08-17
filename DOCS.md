Use this plugin to deploy Docker images to [Google Container Engine (GKE)][gke].

[gke]: https://cloud.google.com/container-engine/

## Overview

The following parameters are used to configure this plugin:

* `image` - this plugin's Docker image
* *optional* `project` - project of the container cluster (defaults to inferring from the service account `token` credential)
* `zone` - zone of the container cluster
* `cluster` - name of the container cluster
* *optional* `namespace` - Kubernetes namespace to operate in (defaults to `default`)
* *optional* `template` - Kubernetes manifest template (defaults to `.kube.yml`)
* *optional* `secret_template` - Kubernetes [_Secret_ resource](http://kubernetes.io/docs/user-guide/secrets/) manifest template (defaults to `.kube.sec.yml`)
* `vars` - variables to use in `template` and `secret_template`
* `secrets` - credential and variables to use in `secret_template` (see [below](#secrets) for details)

### Debugging parameters

These optional parameters are useful for debugging:

* `dry_run` - do not apply the Kubernetes templates (defaults to `false`)
* `verbose` - dump available `vars` and the generated Kubernetes `template` (excluding secrets) (defaults to `false`)

## Credentials

`drone-gke` requires a Google service account and uses it's [JSON credential file][service-account] to authenticate.

This must be passed to the plugin under the target `token`.

The plugin infers the GCP project from the JSON credentials (`token`) and retrieves the GKE cluster credentials.

[service-account]: https://cloud.google.com/storage/docs/authentication#service_accounts

### Setting the JSON token

Improved in Drone 0.5+, it is no longer necessary to align the JSON file.

#### GUI

Simply copy the contents of the JSON credentials file and paste it directly in the input field (for example for a secret named `GOOGLE_CREDENTIALS`).

#### CLI

```
drone secret add \
  --event push \
  --event pull_request \
  --event tag \
  --event deployment \
  --repository org/repo \
  --name GOOGLE_CREDENTIALS \
  --value @gcp-project-name-key-id.json
```

## Secrets

`drone-gke` also supports creating Kubernetes secrets.
These secrets should be passed from Drone secrets to the plugin as environment variables with targets with the prefix `secret_`.
These secrets will be used as variables in the `secret_template` in their environment variable form (uppercased).

Kubernetes expects secrets to be base64 encoded.
If using a secret that is not already base64 encoded, use the `| b64enc` function in the `secret_template` to encode the value.

## Example reference usage

### `.drone.yml`

Note particularly the `gke:` build step.

```yml
---
pipeline:
  build:
    image: golang:1.8
    environment:
      - GOPATH=/drone
      - CGO_ENABLED=0
    commands:
      - go get -t
      - go test -v -cover
      - go build -v -a
    when:
      event:
        - push
        - pull_request

  gcr:
    image: plugins/docker
    storage_driver: overlay
    username: _json_key
    registry: us.gcr.io
    repo: us.gcr.io/my-gke-project/my-app
    tag: ${DRONE_COMMIT}
    secrets:
      - source: GOOGLE_CREDENTIALS
        target: docker_password
    when:
      event: push
      branch: master

  gke:
    image: nytimes/drone-gke
    zone: us-central1-a
    cluster: my-gke-cluster
    namespace: ${DRONE_BRANCH}
    vars:
      image: gcr.io/my-gke-project/my-app:${DRONE_COMMIT}
      app: my-app
      env: dev
    secrets:
      - source: GOOGLE_CREDENTIALS
        target: token
      - source: API_TOKEN        # value is "123"
        target: secret_api_token
      - source: P12_CERT         # value is "cDEyCg=="
        target: secret_p12_cert
    when:
      event: push
      branch: master
```

### `.kube.yml`

Note the two Kubernetes yml resource manifests separated by `---`.

```yml
---
apiVersion: extensions/v1beta1
kind: Deployment

metadata:
  name: {{.app}}-{{.env}}

spec:
  replicas: 3
  template:
    metadata:
      labels:
        app: {{.app}}
        env: {{.env}}
    spec:
      containers:
        - name: app
          image: {{.image}}
          ports:
            - containerPort: 8000
          env:
            - name: APP_NAME
              value: {{.app}}
            - name: API_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{.app}}-{{.env}}
                  key: api-token
---
apiVersion: v1
kind: Service

metadata:
  name: {{.app}}-{{.env}}

spec:
  type: LoadBalancer
  selector:
    app: {{.app}}
    env: {{.env}}
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 8000
```

### `.kube.sec.yml`

Note that the templated output will not be dumped when debugging.

```yml
---
apiVersion: v1
kind: Secret

metadata:
  name: {{.app}}-{{.env}}

type: Opaque

data:
  api-token: {{.SECRET_API_TOKEN | b64enc}} # need to b64 encode this
  p12-cert: {{.SECRET_P12_CERT}}            # already b64 encoded
```
