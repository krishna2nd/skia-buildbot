#!/usr/bin/env python
# Copyright (c) 2013 The Chromium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Module that polls the skia-telemetry AppEngine WebApp."""


import json
import os
import subprocess
import sys
import tempfile
import time
import traceback
import urllib

import appengine_constants


SLEEP_BETWEEN_POLLS_SECS = 60

ADMIN_ENCOUNTERED_KEYS = {}
LUA_ENCOUNTERED_KEYS = {}


def process_admin_tasks(pending_tasks):
  for key in sorted(pending_tasks.keys()):
    task = pending_tasks[key]

    # Extract required parameters.
    task_key = task['key']
    task_name = task['task_name']
    username = task['username']

    log_file = os.path.join(tempfile.gettempdir(), '%s-%s-%s.output' % (
        username, task_name, task_key))
    print 'Output will be available in %s' % log_file

    cmd = ''
    if task_name == appengine_constants.CHROME_ADMIN_TASK_NAME:
      cmd = 'bash vm_build_chromium.sh %s %s %s' % (
          username, task_key, log_file)
    elif task_name == appengine_constants.PAGESETS_ADMIN_TASK_NAME:
      cmd = 'bash vm_create_pagesets_on_slaves.sh %s %s' % (
          username, task_key)
    elif task_name == appengine_constants.WEBPAGE_ARCHIVES_ADMIN_TASK_NAME:
      cmd = 'bash vm_capture_archive_on_slaves.sh %s %s' % (
          username, task_key)
    subprocess.Popen(cmd.split(), stdout=open(log_file, 'w'),
                     stderr=open(log_file, 'w'))


def process_lua_tasks(pending_tasks):
  for key in sorted(pending_tasks.keys()):
    task = pending_tasks[key]
    task_key = task['key']
    if task_key in LUA_ENCOUNTERED_KEYS:
      '%s is already being processed' % task_key
      continue
    LUA_ENCOUNTERED_KEYS[task_key] = 1
    # Create a run id.
    run_id = '%s-%s' % (task['username'].split('@')[0], time.time())
    lua_file = os.path.join(tempfile.gettempdir(), '%s.lua' % run_id)
    f = open(lua_file, 'w')
    f.write(task['lua_script'])
    f.close()

    # Now call the vm_run_lua_on_slaves.sh script.
    log_file = os.path.join(tempfile.gettempdir(), '%s.output' % run_id)
    cmd = 'bash vm_run_lua_on_slaves.sh %s %s %s %s' % (
        lua_file, run_id, task['username'], task_key)
    print 'Output will be available in %s' % log_file
    subprocess.Popen(cmd.split(), stdout=open(log_file, 'w'),
                     stderr=open(log_file, 'w'))


TASK_SUBPATHS_TO_PROCESSING_METHOD = {
    appengine_constants.GET_ADMIN_TASKS_SUBPATH: process_admin_tasks,
    appengine_constants.GET_LUA_TASKS_SUBPATH: process_lua_tasks,
}


class Poller(object):

  def Poll(self):
    while True:
      try:
        for task_subpath, processing_method in TASK_SUBPATHS_TO_PROCESSING_METHOD.items():
          get_tasks_page = urllib.urlopen(
              appengine_constants.SKIA_TELEMETRY_WEBAPP +
              task_subpath)
          pending_tasks = json.loads(
              get_tasks_page.read().replace('\r\n', '\\r\\n'))
          processing_method(pending_tasks)

        print 'Sleeping %s secs' % SLEEP_BETWEEN_POLLS_SECS
        time.sleep(SLEEP_BETWEEN_POLLS_SECS)
      except Exception:
        # The poller should never crash, output the exception and move on.
        print traceback.format_exc()
        continue


if '__main__' == __name__:
  sys.exit(Poller().Poll())
