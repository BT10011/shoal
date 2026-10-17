#!/bin/sh
# Submits the manifests scripts/winget-manifests.sh wrote to
# microsoft/winget-pkgs, as a pull request from the token owner's fork of
# it, for the winget workflow:
#
#   GH_TOKEN=<WINGET_TOKEN> scripts/winget-pr.sh v2.0.0 winget
#
# The pull request is a draft: winget-pkgs' checklist asks for the
# manifests to be validated and installed on Windows first, which only the
# maintainer can do, and for Microsoft's licence agreement to be signed.
# Running it again for the same release replaces the branch's commit, and
# so updates a pull request already open. It works through GitHub's API,
# so the fork, which is very large, is never cloned.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: GH_TOKEN=... $0 <tag> <manifest directory>" >&2
	exit 2
fi
tag=$1
dir=$2
version=${tag#v}
id=BT10011.Shoal
path=manifests/b/BT10011/Shoal/$version
upstream=microsoft/winget-pkgs

for f in "$id.yaml" "$id.installer.yaml" "$id.locale.en-US.yaml"; do
	if [ ! -f "$dir/$f" ]; then
		echo "winget-pr: $dir/$f is missing" >&2
		exit 1
	fi
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

login=$(gh api user --jq .login)
fork=$login/winget-pkgs
parent=$(gh api "repos/$fork" --jq '.parent.full_name // ""' 2>/dev/null || true)
if [ "$parent" != "$upstream" ]; then
	echo "winget-pr: $fork is not a fork of $upstream. Fork it first, as $login." >&2
	exit 1
fi

if gh api "repos/$upstream/contents/manifests/b/BT10011/Shoal" >/dev/null 2>&1; then
	title="Update: $id to $version"
else
	title="New package: $id version $version"
fi

# Branch from winget-pkgs as it is now, so the pull request changes only
# shoal's three files.
gh api -X POST "repos/$fork/merge-upstream" -f branch=master >/dev/null
head=$(gh api "repos/$fork/git/ref/heads/master" --jq .object.sha)
tree=$(gh api "repos/$fork/git/commits/$head" --jq .tree.sha)
for f in "$id.yaml" "$id.installer.yaml" "$id.locale.en-US.yaml"; do
	jq -n --arg path "$path/$f" --rawfile content "$dir/$f" \
		'{path: $path, mode: "100644", type: "blob", content: $content}'
done | jq -s --arg base "$tree" '{base_tree: $base, tree: .}' >"$tmp/tree.json"
tree=$(gh api "repos/$fork/git/trees" --input "$tmp/tree.json" --jq .sha)
commit=$(jq -n --arg message "$title" --arg tree "$tree" --arg parent "$head" \
	'{message: $message, tree: $tree, parents: [$parent]}' |
	gh api "repos/$fork/git/commits" --input - --jq .sha)

branch=$id-$version
if gh api "repos/$fork/git/ref/heads/$branch" >/dev/null 2>&1; then
	gh api -X PATCH "repos/$fork/git/refs/heads/$branch" -f sha="$commit" -F force=true >/dev/null
else
	gh api "repos/$fork/git/refs" -f ref="refs/heads/$branch" -f sha="$commit" >/dev/null
fi

url=$(gh api "repos/$upstream/pulls?state=open&head=$login:$branch" --jq '.[0].html_url // ""')
if [ -n "$url" ]; then
	echo "Updated the pull request already open: $url"
else
	# winget-pkgs' own template, so its checklist is there to tick.
	about="Shoal $version, LAN discovery in the terminal. The manifests were written by Shoal's release workflow from the release's SHA256SUMS: https://github.com/BT10011/shoal/releases/tag/$tag"
	gh api -H "Accept: application/vnd.github.raw+json" \
		"repos/$upstream/contents/.github/PULL_REQUEST_TEMPLATE.md" |
		awk -v about="$about" '{ print } /^## .*Description/ { print ""; print about }' >"$tmp/body.md"
	url=$(jq -n --arg title "$title" --arg head "$login:$branch" --rawfile body "$tmp/body.md" \
		'{title: $title, head: $head, base: "master", body: $body, draft: true, maintainer_can_modify: true}' |
		gh api "repos/$upstream/pulls" --input - --jq .html_url)
	echo "Opened a draft pull request: $url"
fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	echo "winget: $title, $url (a draft until it has been tested on Windows)" >>"$GITHUB_STEP_SUMMARY"
fi
