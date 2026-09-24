package lja

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
)

const (
	replacementNamePrefix = "lja-new-"
	backupNamePrefix      = "lja-old-"
	transactionNameLength = 12
	limaStartOperation    = "start"
	limaRenameOperation   = "rename"
	limaForceFlag         = "-f"
)

func CreateVM(project string, options WorkflowOptions) (returnedInstance LimaInstance, returnErr error) {
	cleanup, err := options.ownGPGWorkflow()
	if err != nil {
		return LimaInstance{}, err
	}
	defer cleanup(&returnErr)
	githubCleanup := func(*error) {}
	defer func() { githubCleanup(&returnErr) }()
	ensureGitHub := func() error {
		cleanup, err := options.ownGitHubWorkflow()
		if err != nil {
			return err
		}
		githubCleanup = cleanup
		return nil
	}
	canonicalProject, err := canonicalProjectPath(project)
	if err != nil {
		return LimaInstance{}, err
	}
	if err := validateProjectDirectory(canonicalProject); err != nil {
		return LimaInstance{}, err
	}
	vmName, err := projectVMName(canonicalProject)
	if err != nil {
		return LimaInstance{}, err
	}
	var result LimaInstance
	err = withAdvisoryLock(canonicalProject, vmName, options.StateRoot, options.LockDirectory, func(string) error {
		if !options.Recreate {
			instance, err := inspectLima(vmName, options.limaCommand())
			if err != nil {
				return err
			}
			if instance != nil {
				if err := validateProjectMount(*instance, canonicalProject, options.StateRoot); err != nil {
					return err
				}
				agentNames, err := configuredAgentNames(options.Development)
				if err != nil {
					return err
				}
				if len(agentNames) == 0 {
					result = *instance
					return nil
				}
				if err := ensureGitHub(); err != nil {
					return err
				}
				if instance.Status == limaStatusStopped {
					slog.Info("starting VM", "vm", vmName)
					startOptions := options.gpg.processOptions(options.limaCommand())
					if _, err := runLima([]string{limaStartOperation, vmName}, startOptions); err != nil {
						return err
					}
					instance, err = inspectLima(vmName, options.limaCommand())
					if err != nil {
						return err
					}
					if instance == nil {
						return ljaError("Lima VM %s disappeared during agent preparation", vmName)
					}
				}
				if instance.Status != limaStatusRunning {
					return ljaError("VM %s is in incompatible state %s", vmName, instance.Status)
				}
				if err := prepareConfiguredAgentsLocked(canonicalProject, vmName, options.StateRoot, options.Development, options.agentTrustDirectories, options.LockDirectory, options.limaCommand(), "", true, options.gpg); err != nil {
					return err
				}
				result = *instance
				return nil
			}
		}
		if err := ensureGitHub(); err != nil {
			return err
		}
		if options.Environment == nil {
			config := developmentConfigOrDefault(options.Development)
			environment, err := config.ResolveEnvironment(environmentMap())
			if err != nil {
				return err
			}
			options.Environment = environment
		}
		instance, err := options.prepareVMLocked(canonicalProject, vmName)
		if err != nil {
			return err
		}
		result = instance
		return nil
	})
	if err != nil {
		return LimaInstance{}, err
	}
	return result, nil
}

func (options WorkflowOptions) prepareVMLocked(project, vmName string) (LimaInstance, error) {
	if options.Recreate {
		old, err := inspectLima(vmName, options.limaCommand())
		if err != nil {
			return LimaInstance{}, err
		}
		if old != nil {
			return options.replaceVMLocked(project, *old)
		}
	}
	return prepareNamedVMLocked(project, vmName, options.StateRoot, options.Development, options.Environment, options.agentTrustDirectories, options.LockDirectory, options.limaCommand(), options.agentUpdateName, true, options.gpg, options.github)
}

type vmReplacement struct {
	options   WorkflowOptions
	project   string
	old       LimaInstance
	candidate string
	backup    string
}

func (replacement vmReplacement) operation(arguments ...string) error {
	_, err := runLima(arguments, defaultProcessOptions(replacement.options.limaCommand()))
	return err
}

func (options WorkflowOptions) replaceVMLocked(project string, old LimaInstance) (result LimaInstance, returnErr error) {
	if old.Status == limaStatusInstalling {
		return LimaInstance{}, ljaError("VM %s is still installing; wait before recreating it", old.Name)
	}
	probe := defaultProcessOptions(options.limaCommand())
	probe.captureOutput = true
	if _, err := runLima([]string{limaRenameOperation, "--help"}, probe); err != nil {
		return LimaInstance{}, ljaError("VM recreation requires limactl rename: %w", err)
	}
	replacement := vmReplacement{options: options, project: project, old: old,
		candidate: replacementNamePrefix + strings.ToLower(rand.Text()[:transactionNameLength]),
		backup:    backupNamePrefix + strings.ToLower(rand.Text()[:transactionNameLength])}
	for _, name := range []string{replacement.candidate, replacement.backup} {
		instance, err := inspectLima(name, options.limaCommand())
		if err != nil {
			return LimaInstance{}, err
		}
		if instance != nil {
			return LimaInstance{}, ljaError("temporary VM name %s already exists; retry recreation", name)
		}
	}
	slog.Info("preparing replacement VM", "vm", old.Name, "replacement", replacement.candidate, "backup", replacement.backup)
	cleanupCandidate := true
	defer func() {
		if returnErr != nil && cleanupCandidate {
			returnErr = errors.Join(returnErr, options.gpg.closeSession())
			if err := replacement.removeCandidate(); err != nil {
				returnErr = errors.Join(returnErr, ljaError("cannot remove failed replacement %s: %w", replacement.candidate, err))
			}
		}
	}()
	if _, err := prepareNamedVMLocked(project, replacement.candidate, options.StateRoot, options.Development, options.Environment, options.agentTrustDirectories, options.LockDirectory, options.limaCommand(), options.agentUpdateName, false, options.gpg, options.github); err != nil {
		return LimaInstance{}, err
	}
	if err := options.gpg.closeSession(); err != nil {
		return LimaInstance{}, err
	}
	if err := replacement.operation(stopCommandName, replacement.candidate); err != nil {
		return LimaInstance{}, err
	}
	if old.Status == limaStatusRunning {
		if err := replacement.operation(stopCommandName, old.Name); err != nil {
			return LimaInstance{}, errors.Join(err, replacement.restoreRunning())
		}
	}
	cleanupCandidate = false
	if err := replacement.rename(old.Name, replacement.backup); err != nil {
		return LimaInstance{}, err
	}
	if err := replacement.rename(replacement.candidate, old.Name); err != nil {
		return LimaInstance{}, err
	}
	instance, err := replacement.startAndValidate()
	if err != nil {
		return LimaInstance{}, errors.Join(err, replacement.rollback())
	}
	if err := replacement.operation(deleteCommandName, limaForceFlag, replacement.backup); err != nil {
		return LimaInstance{}, ljaError("replacement VM %s is ready, but backup %s could not be deleted: %w", old.Name, replacement.backup, err)
	}
	if err := prepareConfiguredAgentsLocked(project, instance.Name, options.StateRoot, options.Development, options.agentTrustDirectories, options.LockDirectory, options.limaCommand(), "", true); err != nil {
		return LimaInstance{}, ljaError("replacement VM %s is ready, but configured agent preparation failed: %w", old.Name, err)
	}
	slog.Info("replaced VM", "vm", old.Name)
	return instance, nil
}

func (replacement vmReplacement) rename(from, to string) error {
	if err := replacement.operation(limaRenameOperation, limaNoninteractiveFlag, from, to); err != nil {
		return ljaError("rename %s to %s failed and may be partial; preserved VM directories for manual recovery (original=%s replacement=%s backup=%s). Inspect them with limactl list and recover the complete VM with limactl rename before retrying; do not delete either side of a partial rename: %w", from, to, replacement.old.Name, replacement.candidate, replacement.backup, err)
	}
	return nil
}

func (replacement vmReplacement) startAndValidate() (LimaInstance, error) {
	if err := replacement.operation(limaStartOperation, replacement.old.Name); err != nil {
		return LimaInstance{}, err
	}
	instance, err := inspectLima(replacement.old.Name, replacement.options.limaCommand())
	if err != nil {
		return LimaInstance{}, err
	}
	if instance == nil {
		return LimaInstance{}, ljaError("replacement VM %s disappeared", replacement.old.Name)
	}
	if err := validateProjectMount(*instance, replacement.project, replacement.options.StateRoot); err != nil {
		return LimaInstance{}, err
	}
	if instance.Status != limaStatusRunning {
		return LimaInstance{}, ljaError("replacement VM %s did not reach Running state", instance.Name)
	}
	if err := verifyGuestConnection(replacement.project, instance.Name, replacement.options.limaCommand()); err != nil {
		return LimaInstance{}, err
	}
	return *instance, nil
}

func (replacement vmReplacement) restoreRunning() error {
	if replacement.old.Status != limaStatusRunning {
		return nil
	}
	instance, err := inspectLima(replacement.old.Name, replacement.options.limaCommand())
	if err != nil {
		return err
	}
	if instance == nil {
		return ljaError("cannot restore missing original VM %s", replacement.old.Name)
	}
	if instance.Status == limaStatusRunning {
		return nil
	}
	return replacement.operation(limaStartOperation, replacement.old.Name)
}

func (replacement vmReplacement) rollback() error {
	instance, err := inspectLima(replacement.old.Name, replacement.options.limaCommand())
	if err != nil {
		return ljaError("cannot inspect failed replacement; backup retained as %s: %w", replacement.backup, err)
	}
	if instance == nil {
		return ljaError("replacement disappeared; backup retained as %s", replacement.backup)
	}
	if instance.Status != limaStatusStopped {
		if err := replacement.operation(stopCommandName, replacement.old.Name); err != nil {
			return ljaError("cannot stop failed replacement; backup retained as %s: %w", replacement.backup, err)
		}
	}
	if err := replacement.rename(replacement.old.Name, replacement.candidate); err != nil {
		return err
	}
	if err := replacement.rename(replacement.backup, replacement.old.Name); err != nil {
		return err
	}
	if err := replacement.restoreRunning(); err != nil {
		return ljaError("original VM name restored but restarting it failed; failed replacement retained as %s: %w", replacement.candidate, err)
	}
	if err := replacement.removeCandidate(); err != nil {
		return ljaError("original VM restored but failed replacement %s could not be deleted: %w", replacement.candidate, err)
	}
	slog.Info("restored original VM", "vm", replacement.old.Name)
	return nil
}

func (replacement vmReplacement) removeCandidate() error {
	instance, err := inspectLima(replacement.candidate, replacement.options.limaCommand())
	if err != nil {
		return err
	}
	if instance == nil {
		return nil
	}
	return replacement.operation(deleteCommandName, limaForceFlag, replacement.candidate)
}
