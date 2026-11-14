---
name: kapten-docs
description: "Read the Kapten Docs instead of answering from memory. Deploy applications, jobs and databases to your own Kubernetes clusters with Kapten. Use when a task involves Kapten Docs: how it works, how to set it up or configure it, or its API."
---

# Kapten Docs

Deploy applications, jobs and databases to your own Kubernetes clusters with Kapten.

This skill points you at the Kapten Docs, at https://docs.kapten.io/. When a task involves Kapten Docs, read the relevant page before relying on what you remember: the docs are the source of truth, and they change.

## Reading the docs

- Any page as Markdown: add `.md` to its URL (the home page is https://docs.kapten.io/index.md). Every page link below already points at the Markdown.
- [llms.txt](https://docs.kapten.io/llms.txt): every page with a one-line summary.
- [llms-full.txt](https://docs.kapten.io/llms-full.txt): every page in one file, for when you need all of it.
- MCP server at https://docs.kapten.io/mcp, with `search_docs`, `get_page`, `list_pages`, and `get_navigation`. Connect it to search the docs instead of guessing URLs.
- [JSON API](https://docs.kapten.io/api/docs/pages.json): the page index, with each page as JSON and Markdown.

## API reference

- [Platform API](https://docs.kapten.io/api/platform), with a page per operation (add `.md` for its Markdown).
- [Auth API](https://docs.kapten.io/api/auth), with a page per operation (add `.md` for its Markdown).

## Pages

- [Kapten documentation](https://docs.kapten.io/index.md): Kapten deploys your applications, jobs and databases to Kubernetes clusters in your own cloud account, with environments, pipelines and secrets built in.
- [Quickstart](https://docs.kapten.io/quickstart.md): Connect a Kubernetes cluster, create a project and an environment, and deploy your first service from a container image.

### Concepts

- [How Kapten works](https://docs.kapten.io/concepts.md): The objects Kapten is built on, from the organization down to the components it deploys, and how they relate to each other.
- [Organizations and roles](https://docs.kapten.io/concepts/organizations-and-roles.md): An organization owns your clusters, projects and integrations; roles decide what each member can see and change.
- [Projects](https://docs.kapten.io/concepts/projects.md): A project groups the services that ship together, like an application and its database, across all of their environments.
- [Environments](https://docs.kapten.io/concepts/environments.md): An environment is one deployment of a project, such as staging or production, running on one cluster with its own components and configuration.
- [Clusters](https://docs.kapten.io/concepts/clusters.md): Kapten deploys into Kubernetes clusters in your own cloud account; you connect each one with its kubeconfig and Kapten installs an agent on it.
- [Components](https://docs.kapten.io/concepts/components.md): Components are what Kapten runs in an environment, applications built from GitHub or pulled from an image, jobs that run once or on a schedule, and databases.
- [Variables and secrets](https://docs.kapten.io/concepts/variables-and-secrets.md): Configure components with environment variables and files, set on a project, an environment or a single component, with secrets stored encrypted.
- [Pipelines](https://docs.kapten.io/concepts/pipelines.md): A pipeline deploys several components of an environment in ordered stages, in parallel within a stage, stopping or continuing when a stage fails.
- [Billing](https://docs.kapten.io/concepts/billing.md): Kapten bills each environment for the minutes it exists, not per seat, and runs on clusters you pay your cloud provider for directly.

### Guides

- [Guides](https://docs.kapten.io/guides.md): Step-by-step instructions for the tasks you do most often with Kapten, from connecting a cluster to deploying with a pipeline.
- [Connect a cluster](https://docs.kapten.io/guides/connect-a-cluster.md): Give Kapten access to a Kubernetes cluster in your cloud account with its kubeconfig, and check that the agent is installed.
- [Deploy from GitHub](https://docs.kapten.io/guides/deploy-from-github.md): Connect your GitHub account, then build and deploy an application from a repository and redeploy it on every push.
- [Deploy a container image](https://docs.kapten.io/guides/deploy-a-container-image.md): Run an application from an existing container image and tag, with its ports, resources and health checks.
- [Add a database](https://docs.kapten.io/guides/add-a-database.md): Create a PostgreSQL, MySQL, MongoDB or Redis database in an environment and connect your application to it with the secrets Kapten generates.
- [Schedule a job](https://docs.kapten.io/guides/schedule-a-job.md): Run a container on a cron schedule, or once, with limits on restarts and duration.
- [Build a pipeline](https://docs.kapten.io/guides/build-a-pipeline.md): Deploy several components of an environment in a set order, for example a migration job, then the API and worker, then the frontend.

### Reference

- [Reference](https://docs.kapten.io/reference.md): Exact values, units and behaviors of Kapten settings, for when you need the details rather than a walkthrough.
- [Resources](https://docs.kapten.io/reference/resources.md): Units for CPU, memory, GPU, storage and instances of applications, jobs and builds, and how requests and limits behave.
- [Environment types](https://docs.kapten.io/reference/environment-types.md): The three environment types, development, staging and production, and what each one is meant for.

### API

- [API overview](https://docs.kapten.io/api.md): Call the Kapten Platform and Auth APIs from scripts and CI with an OAuth application's access token.

### CLI

- [CLI overview](https://docs.kapten.io/cli.md): Install the kapten command-line tool and manage projects, environments and deployments from a terminal, CI jobs and coding agents.
- [Authentication](https://docs.kapten.io/cli/authentication.md): Log in to Kapten from the CLI, switch between organizations with saved contexts, and authenticate CI jobs as an OAuth application.
- [Output and errors](https://docs.kapten.io/cli/output.md): Read kapten results as tables or JSON, filter them for scripts, and handle errors and exit codes.
- [Request bodies](https://docs.kapten.io/cli/request-bodies.md): Pass the fields of a resource to create or update commands, as shorthand arguments or as JSON on stdin.
- [Coding agents](https://docs.kapten.io/cli/mcp.md): Set up a coding agent such as Claude Code, Codex or Cursor to manage Kapten, with the Kapten agent skill and the CLI's local MCP server.

#### Command reference

- [Command reference](https://docs.kapten.io/cli/reference.md): Every kapten command, its arguments and its flags.
- [kapten auth](https://docs.kapten.io/cli/reference/auth.md): Log in, check who commands run as, and switch between saved logins (contexts).
- [kapten applications](https://docs.kapten.io/cli/reference/applications.md): Deploy and run long-running applications.
- [kapten clusters](https://docs.kapten.io/cli/reference/clusters.md): Connect and manage Kubernetes clusters.
- [kapten components](https://docs.kapten.io/cli/reference/components.md): List the components of an environment.
- [kapten databases](https://docs.kapten.io/cli/reference/databases.md): Create and manage databases.
- [kapten environments](https://docs.kapten.io/cli/reference/environments.md): Create and manage the environments of a project.
- [kapten jobs](https://docs.kapten.io/cli/reference/jobs.md): Run and schedule jobs.
- [kapten pipelines](https://docs.kapten.io/cli/reference/pipelines.md): Build and run deployment pipelines.
- [kapten projects](https://docs.kapten.io/cli/reference/projects.md): Create and manage projects.
- [kapten usage](https://docs.kapten.io/cli/reference/usage.md): Show environment usage for billing.
- [kapten mcp](https://docs.kapten.io/cli/reference/mcp.md): Serve Kapten operations as MCP tools to a coding agent, over stdio.
- [kapten skills](https://docs.kapten.io/cli/reference/skills.md): Install the Kapten agent skill and MCP servers for coding agents.
