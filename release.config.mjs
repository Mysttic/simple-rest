// semantic-release, run by .github/workflows/release.yml on every push to
// master. The Conventional Commits since the newest v* tag pick the version
// (feat: minor; fix, perf: patch; "!" or a BREAKING CHANGE footer: major);
// nothing else publishes a release. The release then:
// 1. puts a new section on top of CHANGELOG.md, under the header of the file,
//    which is whatever stands above its first "## " heading;
// 2. writes the version into VERSION.md and runs build.sh, so the binaries,
//    the .deb packages and the checksums carry the new number;
// 3. commits CHANGELOG.md and VERSION.md to master as
//    "chore(release): x.y.z [skip ci]" and tags that commit vx.y.z;
// 4. creates the GitHub release with everything in dist/;
// 5. hands the version to the jobs after it ($GITHUB_OUTPUT).
import { readFileSync } from 'node:fs';

const changelog = readFileSync('CHANGELOG.md', 'utf8');
const firstSection = changelog.search(/^## /m);
const changelogTitle = (firstSection < 0 ? changelog : changelog.slice(0, firstSection)).trim();

// Section names follow the older, hand-written part of CHANGELOG.md.
const types = [
  { type: 'feat', section: 'Dodane' },
  { type: 'fix', section: 'Poprawione' },
  { type: 'perf', section: 'Wydajność' },
  { type: 'revert', section: 'Wycofane' },
  { type: 'docs', section: 'Dokumentacja', hidden: true },
  { type: 'refactor', section: 'Przebudowa', hidden: true },
  { type: 'test', section: 'Testy', hidden: true },
  { type: 'build', section: 'Budowanie', hidden: true },
  { type: 'ci', section: 'CI', hidden: true },
  { type: 'chore', section: 'Porządki', hidden: true },
  { type: 'style', section: 'Styl', hidden: true },
];

export default {
  branches: ['master'],
  repositoryUrl: 'https://github.com/Mysttic/simple-rest.git',
  tagFormat: 'v${version}',
  plugins: [
    ['@semantic-release/commit-analyzer', { preset: 'conventionalcommits' }],
    ['@semantic-release/release-notes-generator', { preset: 'conventionalcommits', presetConfig: { types } }],
    ['@semantic-release/changelog', { changelogFile: 'CHANGELOG.md', changelogTitle }],
    ['@semantic-release/exec', {
      prepareCmd: "printf '%s\\n' '${nextRelease.version}' > VERSION.md && sh build.sh",
      successCmd: 'echo "version=${nextRelease.version}" >> "$GITHUB_OUTPUT"',
    }],
    ['@semantic-release/git', {
      assets: ['CHANGELOG.md', 'VERSION.md'],
      message: 'chore(release): ${nextRelease.version} [skip ci]\n\n${nextRelease.notes}',
    }],
    ['@semantic-release/github', {
      assets: [{ path: 'dist/*' }],
      releaseNameTemplate: 'simple-rest <%= nextRelease.version %>',
      // The report job in release.yml opens one issue for a failure in any
      // job, this one included, so the plugin's own issue and comments stay off.
      successComment: false,
      releasedLabels: false,
      failComment: false,
      failTitle: false,
    }],
  ],
};
