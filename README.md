# GitHub Action Feature Branching

Automates the merging of tagged Pull Requests into a pre-release branch, keeping your main branch clean and stable

## 🚀 Main features

- **Intelligent Automatic Merge:** Combines PRs with a specific tag in a time branch (`pre-main`)
- **Quality Control:** Prevents unapproved merges on the main branch

## ⚙️ Example of use

1. Create the file `.github/workflows/feature-branching.yml`:

  ```yaml
    name: Continuous deployment

    on:
    workflow_dispatch: {}
    pull_request:
      types: [labeled, unlabeled, synchronize, ready_for_review]
      branches: [main]
    push:
      branches: [main]

    permissions:
      contents: write
      pull-requests: read

    jobs:
      merge-features:
        runs-on: ubuntu-latest
        steps:
        - name: Checkout
          uses: actions/checkout@v4
          with:
            fetch-depth: 0

        - name: Merge Labeled PRs
          uses: docker://ghcr.io/josedpiambav/feature-branching:1.0.0
          with:
            github_token: ${{ github.token }}
            owner: ${{ github.repository_owner }}
            repo: ${{ github.event.repository.name }}
            trunk_branch: "main"
            labels: "next-feature"
  ```

## Configuration

|         Input         | Description                                                           |
| :-------------------: | :-------------------------------------------------------------------- |
|     `github_token`      | GitHub token used to authenticate the share and allow you to perform operations on the repository (e.g. read PRs, do merge) |
|     `owner`      | The name of the user or organization that owns the repository where the action is to be executed. |
|     `repo`      | The name of the repository where the action will be executed. |
|     `trunk_brach`      | Name of the main branch that will be used as the basis for the mix of PRs and where no direct merges of features will be made. |
| `labels` | Tag(s) used to filter Pull Requests to be considered for merge in the pre-release branch. If multiple tags are specified, they must be separated by commas.                 |
|  `target_branch`  | Name of the target branch where the tagged PRs will be mixed. If not specified it will take a `pre-{trunk_branch}` value                        |

## Outputs

|  Output   | Description                                                                                            |
| :-------: | :----------------------------------------------------------------------------------------------------- |
|   `target_branch`   | Returns the value of `target_branch`.                                                             |