// Commit messages follow Conventional Commits, because semantic-release reads
// them to pick the next version (see release.config.mjs). The workflow
// .github/workflows/commits.yml checks every commit of a pull request.
// The subject may be in Polish and start with a capital letter.
export default {
  extends: ['@commitlint/config-conventional'],
  rules: {
    'subject-case': [0],
  },
};
