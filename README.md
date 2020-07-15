# ingress-daemonset-controller

`ingress-daemonset-controller` is a Kubernetes controller dedicated to manage ingress controllers.

This might be useful for anyone trying to build:

- A cluster ingress and it needs to be super efficient
- Your own LBaaS on top of Kubernetes and it needs to be super efficient

It provides `IngressDaemonSet` CRD to deploy a set of server application pods so that you can:

1. Run two or more pods per node for high availability of `externalTrafficPolicy: Local` OR `SO_REUSE_PORT`-enabled services.
2. Detach the node from the external lb before updating pods on a node
3. Deploy a dedicated "health-checker" daemonset for more reliable deployment

> Note that the second bullet point becomes unnecessary once https://github.com/kubernetes/kubernetes/issues/85643 is implemented in the upstream.

## Integrations

You'll usually use this to build a highly available and efficient cluster of a reverse proxy or an API gateway like:

- Envoy
- Nginx
- HAProxy
- Caddy
- Skipper
- Kong
- Ambassador
- Istio Ingress Gateway

`ingress-daemonset-controller` isn't a wrapper around any of them, but provides a solid foundation to run it on Kubernetes.

## Pre-requisites

`ingress-daemonset-controller` doesn't manage DNS or VIP for you.

That being said, according to your setup and requirements, you need to prepare any of the followings:

- On a bare-metal env, something like MetalLB needs to be set up to provide VIP to forward traffic to real servers, i.e. K8s nodes running your server apps
- On AWS, Route 53 hosted zone and record sets, and/or CloudFront distributions, and/or ELB to front the real servers
  - For ELB, you'll usually prefer NLB over ALB assuming e.g. L7 routing are handled by your server apps

## Adding replicas to DaemonSet for high-availability

`IngressDaemonSet` has `Spec.Replicas` to configure the number of per-node replicas.

You use a `NodePort` service with `externalTrafficPolicy: Local` OR a `hostNetwork: true` pods to expose pods to receive and serve traffic from the external load balancer.

> Note that in the latter case your applications running in the pods requires `REUSE_PORT` to bind the ports.

> Note that for ingress deployment use-cases, you shouldn't need to use `hostPort` as prevents us from running two or more pods with the same host port and adds perf penalty due to SNAT and DNAT. See the CNI [portmap](https://github.com/containernetworking/plugins/tree/master/plugins/meta/portmap) plugin for more information.

This way, you can deploy an ingress controller or an ingress gateway like `envoy`, `nginx-ingress`, `ambassador`, `istio-gateway` and so on while balancing between availability due to multiple replicas per node, and efficiency due to less network hops.

Usually the former way of using node ports and external traffic policy would work. But in the default Kubernetes setup, a NodePort service may suffer from scalability limit due to iptables contrack table.
If you need to care that, you shall use a CNI plugin that provides an alternative service implementation that doesn't relies on iptables.

For example, Cilium provives the services implementation backed by XDP. In addition to that, Cilium 1.7 or later implements DSR for pod-svc-pod communitation across nodes.
So it might be a good idea to use Cilium anyway to reduce the total number of network hops from the external load balancer to the application pod and vice versa.

Theoretical Performance (The former is better):

- `hostNetwork: true` + `REUSE_PORT`
- `externalTrafficPolicy: Local` + Cilium
- `externalTrafficPolicy: Local` + iptables

Ease of Use (The former is better):

- `externalTrafficPolicy: Local` + iptables (No extra component needed. Regular K8s manifests work)
- `externalTrafficPolicy: Local` + Cilium (Cilium needed. Regular K8s manifests work)
- `hostNetwork: true` + `REUSE_PORT` (Requires your app to support REUSE_PORT. The ingress solution of your choice might not provide official example manifests for this setup)

## Reliable deployment

`IngressDaemonSet` is smart enough to let you tell external load balancer to stop flowing traffic to the node before updating pods either by:

- Start failing health-checks and wait grace period
- Start explicitly detaching the node by annotating the node with a `node-detacher` annotation and wait grace period or wait until `node-detacher` finishes the detachment, so that traffic stops before pod gets deleted and restarted.

The health-checker responds with 200 OK if and only if all the replicas in the node are ready AND there's no rolling update scheduled for the replicas in the node.

In other words, it enables traffic top stop before pod gets deleted and restarted for rolling update, which gives more reliability to your service while updating.

## Example

Here's an example of `IngressDaemonSet`:

```
kind: IngressDaemonSet
apiVersion: ingressdaemonsets.mumoshu.github.io/v1alpha1
metadata:
  name: nginx-ingress
  labels:
    app: ingress-nginx
    app.kubernetes.io/name: ingress-nginx
    app.kubernetes.io/part-of: ingress-nginx
    kubernetes.io/cluster-service: "true"
spec:
  # NEW: 2 pods per node (See below)
  podsPerNode: 2

  # NEW: The host/node port to be used for responding health-check http requests from the external load balancer
  healthCheckNodePorts:
  - 10080

  updateStrategy:
    # Has different meaning and options than standard DaemonSet (See below)
    type: RollingUpdate
    rollingUpdate:
      # If this is greater than or equal to 1, the controller communicates with node-detacher to detach the node
      # and makes the node unavailable to the external lb before updating pods on the node
      #
      # If this is set, `maxUnavaiablePodsPerNode` is automatically set to `100%` and `maxDegradedNodes` must be `0`,
      # which means the controller updates all the pods in a node concurrently after detaching the node from external lb, which is safe and can be faster than the other method.
      #
      # The default value is 0, which means it relies on maxUnavaiablePodsPerNode and maxDegaradedNodes only.
      maxUnavailableNodes: 2

      # The controller runs rolling-update of pods up to 3 pods on the node
      maxUnavaiablePodsPerNode: 3

      # maxSurgedPodsPerNode is the maxSurge for the per-node deployment
      maxSurgedPodsPerNode: 1

      # The controller runs rolling-updates of currently on and pods up to 3 nodes.
      # The default is 1. You can only set either of `maxUnavaiableNodes` or `maxDegradedNodes` to 1 or greater.
      maxDegradedNodes: 3

      # The controller annotates the per-node deployment to start failing health-checks from the external lb
      # even before any pod gets replaced. 
      #annotateDeploymentToDetach:
      #  key: "ingressdaemonsets.mumoshu.github.com/to-be-updated"
      #  gracePeriodSeconds: 10

      # annotateNodeToDetach configures the controller to annotate the node before updating pods scheduled onto the node.
      # It can either be (1) annotation key or (2) annotation key=value.
      # When the first option is used, the controller annotate the node with the specified key, without an empty value.
      annotateNodeToDetach:
        key: "node-detacher.variant.run/detached"
        value: "true"
        gracePeriodSeconds: 10

      # waitForDetachmentByAnnotatedTimestamp configures the controller to wait for the certain period since the detachment
      # timestamp stored in the specified annotation.
      # This depends on and requires configuring AnnotateNodeToDetach, too.
      waitForDetachmentByAnnotatedTimestamp:
        key: "node-detacher.variant.run/detachment-timestamp"
        format: RFC3339
        gracePeriodSeconds: 30
  healthChecker:
    image: mumoshu/ingress-daemonset-controller:latest
    imagePullPolicy: Always
    serviceAccountName: default
  template:
    metadata:
      labels:
        app: ingress-nginx
        app.kubernetes.io/name: ingress-nginx
        app.kubernetes.io/part-of: ingress-nginx
      annotations:
        prometheus.io/port: '10254'
        prometheus.io/scrape: 'true'
    spec:
      nodeSelector:
        ingress-controller-node: "true"
      hostNetwork: true
      terminationGracePeriodSeconds: 300
      serviceAccountName: nginx-ingress-serviceaccount
      containers:
        - name: nginx-ingress-controller
          image: quay.io/kubernetes-ingress-controller/nginx-ingress-controller:0.26.1
          imagePullPolicy: Always
          livenessProbe:
            failureThreshold: 3
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
            initialDelaySeconds: 10
            periodSeconds: 10
            successThreshold: 1
            timeoutSeconds: 10
          readinessProbe:
            failureThreshold: 3
            httpGet:
              path: /healthz
              port: 10254
              scheme: HTTP
            periodSeconds: 10
            successThreshold: 1
            timeoutSeconds: 10
          lifecycle:
            preStop:
              exec:
                command:
                  - /wait-shutdown
          args:
            - /nginx-ingress-controller
            - --default-backend-service=$(POD_NAMESPACE)/default-http-backend
            - --configmap=$(POD_NAMESPACE)/nginx-configuration
            - --tcp-services-configmap=$(POD_NAMESPACE)/tcp-services
            - --udp-services-configmap=$(POD_NAMESPACE)/udp-services
            - --publish-service=$(POD_NAMESPACE)/ingress-nginx
            - --annotations-prefix=nginx.ingress.kubernetes.io
          securityContext:
            allowPrivilegeEscalation: true
            capabilities:
              drop:
                - ALL
              add:
                - NET_BIND_SERVICE
            # www-data -> 33
            runAsUser: 33
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
          - name: http
            containerPort: 80
            hostPort: 80
          - name: https
            containerPort: 443
            hostPort: 443
```

So spec-wise, the only difference between `IngressDaemonSet` and standard `DaemonSet` is that the former adds `replicas`.

`type: RollingUpdate` update strategy has the alternative meaning in `IngressDaemonSet`. It now relies on a "health-checker daemonset` managed by the controller to stop the external lb to stop flowing traffc to the node while any of the pods on the node is not ready.
For the above example, due to `healthCheckPort: 10080`, the health-checker daemonset pods bind the port 10080 on the host for responding health-check HTTP requests from the external loadbalancer.

It can optionally rely on [node-detacher](https://github.com/mumoshu/node-detacher/) too, to stop the external lb to stop flowing traffic to the node BEFORE any of the pods on the node start stopping, which gives you extra grace period to stop the traffic compared to the case that you relied only on the health-checker daemonset.

## How it works

The controller deploys a deployment per node and daemonset per cluster for each `IngressDaemonSet`.

The per-node deployment is used to manage daemonset pod replicas for each node.

The generated deployment spec has the pod template that is mostly equivalent to `IngressDaemonSet.Spec.Template`.

`IngressDaemonSet.Spec.UpdateStrategy.RollingUpdate.MaxUnavaiablePodsPerNode` is literally copied to `Deployment.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable`.

`Deployment.Spec.Template.NodeName` is set to the node that the per-node deployment is responsible for, so that pods short-circuit the scheduler and gets scheduled onto the target node without stressing the scheduler.
The deployment's name, selector (matchLabels), and pod labels are configured so that they don't collide with another per-node deployment.
The deployment's name is `<INGRESS_DS_NAME>-<NODE_NAME>`. The deployment labels contains `IngressDaemonSet.Spec.Selector.MatchLabels` and `ingress-daemon-for-node=NODE_NAME`.
The former is defined by the user and used by the ingress daemonset to differentiate pods across different ingress daemonsets.
The latter is automatically generated by the controller to differentiate deployments across different nodes.

The per-cluster daemonset is dedicated to serve externa loadbalancer health-checks.

The daemonset is named `<INGRESS_DS_NAME>-<PORT NUMBER>`. It creates pods from one of the health-checker container images that are published as the part of the ingress-daemonset-controller's release.

Each pod uses the host network and binds the port designated by `IngressDaemonSet.Spec.HealthCheckNodePort` to serve external loadbalancer health-checks.

You'll either:

1. Point service loadbalancers `Service.Spec.HealthCheckNodePort` to ingerss daemonset's health-check node port, or
2. Configure the external loadbalancer via cloud-provider-specific API to target health-check requests to the health-check node port

If you're using one of public clouds that has a solid L4 and/or L7 loadbalancers, it is recommended to use the second option so that you can reuse the external loadbalancer beyond cluster replacements.

If you're using ingress daemonset to implement a load-balancer-as-a-service in an on-premise infrastructure, it can be correct to use the first due to various reasons,
like there's no easy way to continually attach set of K8s nodes to a LB without Kubernetes.

You don't need to fine-tune `prestop` hooks for your containers anymore, as the controller annotates the per-node deployment to make the health-check failing and wait for a configurable grace period, even before any pod gets deleted and recreated.
Please see `annotateDeploymentToDetach` in the above example for more information.
