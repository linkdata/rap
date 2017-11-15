#ifndef RAP_HEADER_HPP
#define RAP_HEADER_HPP

#include "rap.hpp"

#include <cassert>
#include <cstdint>

namespace rap {

class header {
 public:
  // these masks apply to buf_[2]
  static const unsigned char mask_final = 0x80;
  static const unsigned char mask_head = 0x40;
  static const unsigned char mask_body = 0x20;
  static const unsigned char mask_id = (rap_conn_exchange_id >> 8);

  header() {
    buf_[0] = 0;
    buf_[1] = 0;
    buf_[2] = 0;
    buf_[3] = 0;
  }

  explicit header(uint16_t id) {
    buf_[0] = 0;
    buf_[1] = 0;
    buf_[2] = static_cast<unsigned char>((id >> 8) & mask_id);
    buf_[3] = static_cast<unsigned char>(id);
  }

  const char *data() const { return reinterpret_cast<const char *>(this); }
  char *data() { return reinterpret_cast<char *>(this); }
  size_t size() const { return rap_frame_header_size + payload_size(); }

  bool has_payload() const { return (buf_[2] & (mask_head | mask_body)) != 0; }
  size_t payload_size() const { return has_payload() ? size_value() : 0; }

  size_t size_value() const {
    return static_cast<size_t>(buf_[0]) << 8 | buf_[1];
  }
  void set_size_value(size_t n) {
    buf_[0] = static_cast<unsigned char>(n >> 8);
    buf_[1] = static_cast<unsigned char>(n);
  }

  uint16_t id() const {
    return static_cast<uint16_t>(buf_[2] & mask_id) << 8 | buf_[3];
  }
  void set_id(uint16_t id) {
    buf_[2] = static_cast<unsigned char>(id >> 8) & mask_id;
    buf_[3] = static_cast<unsigned char>(id);
  }

  bool is_conn_control() const { return buf_[2] == mask_id && buf_[3] == 0xff; }
  bool is_final() const { return (buf_[2] & mask_final) != 0; }
  void set_final() { buf_[2] |= mask_final; }
  void clr_final() { buf_[2] &= ~mask_final; }
  bool has_head() const { return (buf_[2] & mask_head) != 0; }
  void set_head() { buf_[2] |= mask_head; }
  bool has_body() const { return (buf_[2] & mask_body) != 0; }
  void set_body() { buf_[2] |= mask_body; }

 private:
  unsigned char buf_[4];
};

}  // namespace rap

#endif  // RAP_HEADER_HPP
