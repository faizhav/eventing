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

#ifndef QUERY_MGR_H
#define QUERY_MGR_H

#include "conn-pool.h"
#include "query-iterable.h"

#include <libcouchbase/couchbase.h>
#include <string>
#include <unordered_map>
#include <v8.h>

#include "info.h"
#include "query-helper.h"

namespace Query {
class Manager {
public:
  explicit Manager(v8::Isolate *isolate, const std::string &conn_str,
                   const std::size_t pool_size)
      : isolate_(isolate), conn_pool_(pool_size, conn_str, isolate) {}
  ~Manager() { ClearQueries(); }

  Manager() = delete;
  Manager(Manager &&) = delete;
  Manager(const Manager &) = delete;
  Manager &operator=(const Manager &) = delete;
  Manager &operator=(Manager &&) = delete;

  Iterable::Info NewIterable(const Query::Info &query_info);
  void ClearQueries();
  void RestoreConnection(lcb_t connection) {
    conn_pool_.RestoreConnection(connection);
  }

private:
  v8::Isolate *isolate_;
  Connection::Pool conn_pool_;
  std::unordered_map<lcb_t, std::unique_ptr<Iterator>> iterators_;
};
} // namespace Query

void QueryFunction(const v8::FunctionCallbackInfo<v8::Value> &args);

#endif
