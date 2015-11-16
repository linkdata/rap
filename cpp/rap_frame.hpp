#ifndef RAP_FRAME_HPP
#define RAP_FRAME_HPP

// #include "rap.hpp"
#include "rap_header.hpp"
#include "rap_text.hpp"

#include <cassert>
#include <cstdint>

namespace rap {

struct frame {
  static size_t needed_bytes(const char* src_ptr) {
    return reinterpret_cast<const rap::header*>(src_ptr)->size();
  }

  static frame* factory(const char* src_ptr, size_t src_len) {
    assert(src_ptr != NULL);
    assert(rap_frame_header_size <= src_len);
    assert(needed_bytes(src_ptr) == src_len);
    return reinterpret_cast<const frame*>(src_ptr)->copy();
  }

  frame* copy() const {
    size_t src_len = size();
    if (frame* f = static_cast<frame*>(malloc(src_len))) {
      memcpy(f, this, src_len);
      assert(f->size() == src_len);
      return f;
    }
    return NULL;
  }

  const rap::header& header() const { return *reinterpret_cast<const rap::header*>(this); }
  rap::header& header() { return *reinterpret_cast<rap::header*>(this); }
  const char* data() const { return reinterpret_cast<const char*>(this); }
  char* data() { return reinterpret_cast<char*>(this); }
  size_t size() const { return header().size(); }

  bool has_payload() const { return header().has_payload(); }
  size_t payload_size() const { return header().payload_size(); }
  const char* payload() const { return data() + rap_frame_header_size; }
  char* payload() { return data() + rap_frame_header_size; }
};

struct framelink {
  static void enqueue(framelink** pp_fl, const frame* f) {
    if (f != NULL) {
      size_t framesize = f->size();
      if (framelink* p_fl = reinterpret_cast<framelink*>(malloc(sizeof(framelink) + framesize))) {
        p_fl->next = NULL;
        memcpy(p_fl + 1, f, framesize);
        while (*pp_fl) pp_fl = &((*pp_fl)->next);
        *pp_fl = p_fl;
      }
    }
  }

  static void dequeue(framelink** pp_fl) {
    while (framelink* p_fl = *pp_fl) {
      if (p_fl->next == NULL) {
        *pp_fl = NULL;
        free(p_fl);
        return;
      }
      pp_fl = &(p_fl->next);
    }
    return;
  }

  framelink* next;
};


} // namespace rap

#endif // RAP_FRAME_HPP
