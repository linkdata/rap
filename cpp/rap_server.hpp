#ifndef RAP_SERVER_HPP
#define RAP_SERVER_HPP

#include "rap.hpp"

#include <cstdint>

namespace rap {

template <typename server_t, typename conn_t, typename exchange_t>
class server {
public:
  server()
    : stat_head_count_(0)
    , stat_read_iops_(0)
    , stat_read_bytes_(0)
    , stat_write_iops_(0)
    , stat_write_bytes_(0)
  {}

  uint64_t stat_head_count() const { return stat_head_count_; }
  void stat_head_count_inc() { ++stat_head_count_; }

  uint64_t stat_read_iops() const { return stat_read_iops_; }
  uint64_t stat_read_bytes() const { return stat_read_bytes_; }
  void stat_read_bytes_add(uint64_t n) {
    ++stat_read_iops_;
    stat_read_bytes_ += n;
  }

  uint64_t stat_write_iops() const { return stat_write_iops_; }
  uint64_t stat_write_bytes() const { return stat_write_bytes_; }
  void stat_write_bytes_add(uint64_t n) {
    ++stat_write_iops_;
    stat_write_bytes_ += n;
  }

protected:
  uint64_t stat_head_count_;
  uint64_t stat_read_iops_;
  uint64_t stat_read_bytes_;
  uint64_t stat_write_iops_;
  uint64_t stat_write_bytes_;
};

} // namespace rap

#endif // RAP_SERVER_HPP
