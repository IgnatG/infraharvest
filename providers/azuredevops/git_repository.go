package azuredevops

import (
	"github.com/microsoft/azure-devops-go-api/azuredevops/git"
)

type GitRepositoryGenerator struct {
	AzureDevOpsService
}

func (az *GitRepositoryGenerator) listResources() ([]git.GitRepository, error) {

	client, err := az.getGitClient()
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	resources, err := client.GetRepositories(ctx, git.GetRepositoriesArgs{})
	if err != nil {
		return nil, err
	}
	return *resources, nil
}

func (az *GitRepositoryGenerator) appendResource(resource *git.GitRepository) {

	id := *resource.Id
	az.appendSimpleResource(id.String(), *resource.Name, "azuredevops_git_repository")
}

func (az *GitRepositoryGenerator) InitResources() error {

	resources, err := az.listResources()
	if err != nil {
		return err
	}
	for _, resource := range resources {
		az.appendResource(&resource)
	}
	return nil
}
