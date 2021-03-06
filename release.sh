#!/bin/bash

# Updates the Version variables, commits, and "gx release" the package

version="$1"

if [ -z $version ]; then
   echo "Need a version!"
   exit 1  
fi

sed -i "s/const Version.*$/const Version = \"$version\"/" version.go
sed -i "s/const Version.*$/const Version = \"$version\"/" ipfs-cluster-ctl/main.go
git commit -S -a -m "Release $version"
lastver=`git tag -l | grep -E 'v[0-9]+\.[0-9]+\.[0-9]+' | tail -n 1`
echo "Tag for Release ${version}" > tag_annotation
echo >> tag_annotation
git log --pretty=oneline ${lastver}..HEAD >> tag_annotation
git tag -a -s -F tag_annotation v$version
gx release $version 
