#!/usr/bin/env python
# Copyright (c) 2013 The Chromium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

""" Install all executables, and any runtime resources that are needed by
    *both* Test and Bench builders. """

from valgrind_build_step import ValgrindBuildStep
from build_step import BuildStep
from install import Install
import sys


class ValgrindInstall(ValgrindBuildStep, Install):
  pass


if '__main__' == __name__:
  sys.exit(BuildStep.RunBuildStep(ValgrindInstall))
