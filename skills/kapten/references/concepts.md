# Concept model

Read this when choosing a resource, explaining how resources relate, or planning a change across the hierarchy. Each section links the product docs page that holds the details; read it before answering detailed questions.

## Hierarchy

```text
Organization
├── Clusters
├── Members, roles and integrations
└── Projects
    └── Environments ── each runs on one Cluster
        ├── Components: Applications, Jobs, Databases
        ├── Variables and secrets
        └── Pipelines ── ordered Stages of Components
```

IDs are not interchangeable across levels: commands below the organization take the parent IDs as well as the target ID.

## Organization

The tenant and the top-level authorization boundary. Clusters, projects and integrations belong to one organization; members and OAuth applications get roles that decide what they can see and change. A CLI context is bound to the organization chosen at login. Details: https://docs.kapten.io/concepts/organizations-and-roles.md

## Cluster

A Kubernetes cluster in the user's cloud account, connected with its kubeconfig; Kapten installs its agent on it. Its status tells whether it accepts deployments. Environments run on a cluster; workloads never pick a cluster directly. Details: https://docs.kapten.io/concepts/clusters.md

## Project

Groups the services that ship together across their environments. A project can be deleted only once it has no environments. Details: https://docs.kapten.io/concepts/projects.md

## Environment

One deployment of a project (for example staging or production) on one cluster, with its own components, variables and pipelines, billed per minute while it exists. It is the authorization boundary for its contents, and it can be deleted only once empty. Details: https://docs.kapten.io/concepts/environments.md and https://docs.kapten.io/concepts/billing.md

## Components

What runs in an environment:

- **Application**: a long-running service built from a GitHub repository or pulled from a container image, with resources, ports, health checks, storage and scaling.
- **Job**: a container that runs to completion, once or on a cron schedule.
- **Database**: a database engine run by Kapten. The engines and the operations available on them (deploy, backups, restores, upgrades, maintenance) depend on the installed CLI: read `kapten databases --help`.

A component's configuration is separate from its runs: each deploy or execution is a **run** with a status, timing and logs. `last_run_status` is the latest attempt, not the latest success. Query logs per run. Details: https://docs.kapten.io/concepts/components.md

## Variables and secrets

Environment variables and files set on a project, an environment or one component. A change takes effect at the component's next deployment. A secret's value can be replaced but never read back. Creating a database adds its credentials to the environment as `KAPTEN_DATABASE_<NAME>_*` secrets managed by Kapten. Details: https://docs.kapten.io/concepts/variables-and-secrets.md

## Pipeline

Deploys several components of an environment in ordered stages, in parallel within a stage, and stops or continues when a stage fails. A pipeline run aggregates the component runs it started. Editing a pipeline changes future runs only. Details: https://docs.kapten.io/concepts/pipelines.md

## Authorization

The server checks every request against the identity's roles in the organization and the resource hierarchy. A list returns what the caller may see, not the whole organization.
