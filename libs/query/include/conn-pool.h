// Copyright (c) 2019 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an "AS IS"
// BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
// or implied. See the License for the specific language governing
// permissions and limitations under the License.

#ifndef CONN_POOL_H
#define CONN_POOL_H

#include "query-iterator.h"

#include <libcouchbase/couchbase.h>
#include <queue>
#include <string>
#include <utility>
#include <v8.h>

#include "info.h"

namespace Connection {
struct Info : public ::Info {
  Info() = delete;
  Info(bool is_fatal) : ::Info(is_fatal) {}
  Info(bool is_fatal, const std::string &msg) : ::Info(is_fatal, msg) {}
  Info(lcb_t instance) : ::Info(false), connection(instance) {}

  lcb_t connection{nullptr};
};

class Pool {
public:
  explicit Pool(std::size_t capacity, std::string conn_str,
                v8::Isolate *isolate)
      : isolate_(isolate), conn_str_(std::move(conn_str)), capacity_(capacity) {
  }
  ~Pool();

  Pool() = delete;
  Pool(Pool &&) = delete;
  Pool(const Pool &) = delete;
  Pool &operator=(Pool &&) = delete;
  Pool &operator=(const Pool &) = delete;

  Connection::Info GetConnection();
  void RestoreConnection(lcb_t connection);

private:
  Connection::Info CreateConnection();

  v8::Isolate *isolate_;
  std::string conn_str_;
  const std::size_t capacity_;
  std::size_t current_size_{0};
  std::queue<lcb_t> pool_;
  folly::fibers::TimedMutex pool_mutex_;
};
} // namespace Connection

#endif
