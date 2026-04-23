.PHONY: hooks

hooks:
	git config core.hooksPath scripts/githooks
	@echo "Git hooks installed (core.hooksPath=scripts/githooks)."
	@echo "To uninstall: git config --unset core.hooksPath"
